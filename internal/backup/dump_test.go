package backup

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

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
