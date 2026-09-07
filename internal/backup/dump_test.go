package backup

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/metrics"
	_ "modernc.org/sqlite"
)

func TestDumpSQLRoundTrips(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	if _, err := source.Exec(`
		CREATE TABLE samples (ts INTEGER NOT NULL, metric TEXT NOT NULL, value REAL NOT NULL);
		INSERT INTO samples VALUES (1000, 'cpu_percent', 42.5);
		INSERT INTO samples VALUES (2000, 'it''s "quoted"', -3.0);
	`); err != nil {
		t.Fatalf("seed source db: %v", err)
	}
	source.Close()

	dump, err := DumpSQL(sourcePath)
	if err != nil {
		t.Fatalf("DumpSQL: %v", err)
	}
	sqlText := string(dump)
	if !strings.Contains(sqlText, "CREATE TABLE samples") {
		t.Errorf("dump missing CREATE TABLE: %s", sqlText)
	}
	if !strings.Contains(sqlText, "INSERT INTO") {
		t.Errorf("dump missing INSERT statements: %s", sqlText)
	}

	// The real test: replay the dump into a fresh, empty database and
	// confirm it reproduces the exact same rows — not just that the dump
	// text "looks like SQL".
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	restored, err := sql.Open("sqlite", restoredPath)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer restored.Close()
	if _, err := restored.Exec(sqlText); err != nil {
		t.Fatalf("replay dump into fresh database: %v", err)
	}

	rows, err := restored.Query(`SELECT ts, metric, value FROM samples ORDER BY ts`)
	if err != nil {
		t.Fatalf("query restored rows: %v", err)
	}
	defer rows.Close()
	type row struct {
		ts     int64
		metric string
		value  float64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ts, &r.metric, &r.value); err != nil {
			t.Fatalf("scan restored row: %v", err)
		}
		got = append(got, r)
	}
	want := []row{{1000, "cpu_percent", 42.5}, {2000, `it's "quoted"`, -3.0}}
	if len(got) != len(want) {
		t.Fatalf("restored rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDumpSQLEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.Close()

	dump, err := DumpSQL(path)
	if err != nil {
		t.Fatalf("DumpSQL on an empty database: %v", err)
	}
	if !strings.Contains(string(dump), "BEGIN TRANSACTION") {
		t.Errorf("dump = %q, want at least the transaction wrapper", dump)
	}
}

// TestDumpSQLDuringConcurrentWrite reproduces the live failure this
// deployment hit: automatic backups (runBackupScheduler) call DumpSQL
// in-process, on a schedule, on the exact same metrics.db a live
// internal/metrics.Store is writing to — a second, independent
// connection on the same file, not something metrics.Store's own
// SetMaxOpenConns(1) protects against. Before metrics.Open put the
// database in WAL mode, this reproduced "database is locked" reliably.
func TestDumpSQLDuringConcurrentWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	store, err := metrics.Open(path)
	if err != nil {
		t.Fatalf("metrics.Open: %v", err)
	}
	defer store.Close()
	if err := store.Insert(context.Background(), time.Now(), "cpu_percent", 12.5); err != nil {
		t.Fatalf("seed a sample: %v", err)
	}

	// A second, independent connection holding an exclusive lock —
	// BEGIN EXCLUSIVE rather than Go's sql.Tx (a plain deferred
	// transaction, which only briefly needs an exclusive lock at COMMIT,
	// too narrow a window to hit deterministically in a test) forces
	// exactly the kind of lock a concurrent writer can hold for real
	// work: without WAL mode, no other connection — reader or writer —
	// can touch the database at all until it's released.
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open writer connection: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("begin exclusive transaction: %v", err)
	}

	if _, err := DumpSQL(path); err != nil {
		t.Errorf("DumpSQL while another connection holds an exclusive lock: %v (WAL mode should let this read proceed concurrently)", err)
	}

	if _, err := writer.Exec("COMMIT"); err != nil {
		t.Fatalf("commit held transaction: %v", err)
	}
}
