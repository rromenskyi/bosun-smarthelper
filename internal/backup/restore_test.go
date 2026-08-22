package backup

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractArchiveRestoresFilesAndReplaysMetrics(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "memos.json"), []byte(`{"memos":{"a":1}}`), 0o600); err != nil {
		t.Fatalf("write memos.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, "document-images"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "document-images", "a.png"), []byte("img-bytes"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	sourceDB, err := sql.Open("sqlite", filepath.Join(sourceDir, "metrics.db"))
	if err != nil {
		t.Fatalf("open source metrics.db: %v", err)
	}
	if _, err := sourceDB.Exec(`CREATE TABLE samples (ts INTEGER, metric TEXT, value REAL); INSERT INTO samples VALUES (42, 'cpu_percent', 7.5);`); err != nil {
		t.Fatalf("seed source metrics.db: %v", err)
	}
	sourceDB.Close()

	var archive bytes.Buffer
	if err := BuildArchive(&archive, sourceDir); err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "restored")
	if err := ExtractArchive(bytes.NewReader(archive.Bytes()), destDir); err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}

	memos, err := os.ReadFile(filepath.Join(destDir, "memos.json"))
	if err != nil {
		t.Fatalf("read restored memos.json: %v", err)
	}
	if string(memos) != `{"memos":{"a":1}}` {
		t.Errorf("memos.json = %q, want the original content unchanged", memos)
	}
	image, err := os.ReadFile(filepath.Join(destDir, "document-images", "a.png"))
	if err != nil {
		t.Fatalf("read restored image: %v", err)
	}
	if string(image) != "img-bytes" {
		t.Errorf("image = %q, want img-bytes", image)
	}

	if _, err := os.Stat(filepath.Join(destDir, "metrics.sql")); !os.IsNotExist(err) {
		t.Error("metrics.sql should have been removed after being replayed into metrics.db")
	}

	restoredDB, err := sql.Open("sqlite", filepath.Join(destDir, "metrics.db"))
	if err != nil {
		t.Fatalf("open restored metrics.db: %v", err)
	}
	defer restoredDB.Close()
	var ts int64
	var metric string
	var value float64
	if err := restoredDB.QueryRow(`SELECT ts, metric, value FROM samples`).Scan(&ts, &metric, &value); err != nil {
		t.Fatalf("query restored metrics.db: %v", err)
	}
	if ts != 42 || metric != "cpu_percent" || value != 7.5 {
		t.Errorf("restored row = (%d, %q, %v), want (42, cpu_percent, 7.5)", ts, metric, value)
	}
}

func TestExtractArchiveWithoutMetricsSQLSkipsReplay(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	var archive bytes.Buffer
	if err := BuildArchive(&archive, sourceDir); err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "restored")
	if err := ExtractArchive(bytes.NewReader(archive.Bytes()), destDir); err != nil {
		t.Fatalf("ExtractArchive without a metrics.db present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "metrics.db")); !os.IsNotExist(err) {
		t.Error("metrics.db should not exist when the archive had no metrics.sql to replay")
	}
}
