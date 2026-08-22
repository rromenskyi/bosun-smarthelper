package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func readTarGz(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", header.Name, err)
		}
		files[header.Name] = string(content)
	}
	return files
}

func TestBuildArchiveIncludesPlainFilesAndDumpsMetricsDB(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memos.json"), []byte(`{"memos":{}}`), 0o600); err != nil {
		t.Fatalf("write memos.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "document-images"), 0o700); err != nil {
		t.Fatalf("mkdir document-images: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "document-images", "a.png"), []byte("fake-png"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	dbPath := filepath.Join(dir, "metrics.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open metrics.db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE samples (ts INTEGER, metric TEXT, value REAL); INSERT INTO samples VALUES (1, 'x', 1.0);`); err != nil {
		t.Fatalf("seed metrics.db: %v", err)
	}
	db.Close()

	var archive bytes.Buffer
	if err := BuildArchive(&archive, dir); err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}

	files := readTarGz(t, archive.Bytes())
	if files["memos.json"] != `{"memos":{}}` {
		t.Errorf("memos.json = %q, want the original content unchanged", files["memos.json"])
	}
	if files["document-images/a.png"] != "fake-png" {
		t.Errorf("document-images/a.png = %q, want fake-png", files["document-images/a.png"])
	}
	if _, ok := files["metrics.db"]; ok {
		t.Error("archive contains a raw metrics.db copy, want it replaced by metrics.sql")
	}
	sqlDump, ok := files["metrics.sql"]
	if !ok {
		t.Fatal("archive is missing metrics.sql")
	}
	if !strings.Contains(sqlDump, "CREATE TABLE samples") || !strings.Contains(sqlDump, "INSERT INTO") {
		t.Errorf("metrics.sql = %q, missing expected SQL", sqlDump)
	}
}

func TestBuildArchiveWithoutMetricsDBStillWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	var archive bytes.Buffer
	if err := BuildArchive(&archive, dir); err != nil {
		t.Fatalf("BuildArchive without a metrics.db present: %v", err)
	}
	files := readTarGz(t, archive.Bytes())
	if _, ok := files["settings.json"]; !ok {
		t.Error("archive is missing settings.json")
	}
	if _, ok := files["metrics.sql"]; ok {
		t.Error("archive has metrics.sql even though no metrics.db existed to dump")
	}
}
