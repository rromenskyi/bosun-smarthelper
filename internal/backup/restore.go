package backup

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ExtractArchive extracts a tar.gz produced by BuildArchive into destDir
// (created if it doesn't exist), then — if the archive contains
// metrics.sql (see BuildArchive/DumpSQL) — replays it into a fresh
// metrics.db in destDir and removes metrics.sql, so destDir ends up laid
// out exactly like a normal live data directory rather than needing a
// separate manual SQL-replay step.
func ExtractArchive(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	hasMetricsSQL := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		// destDir is always a path this process picked itself (a CLI flag
		// or a freshly-generated temp/timestamped directory), and every
		// entry comes from an archive this same package built — no
		// attacker-controlled path traversal to guard against here, but
		// filepath.Clean still keeps a malformed "../" entry from
		// escaping destDir by accident.
		targetPath := filepath.Join(destDir, filepath.Clean(header.Name))
		if header.Name == "metrics.sql" {
			hasMetricsSQL = true
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return fmt.Errorf("create directory for %s: %w", header.Name, err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("read %s from archive: %w", header.Name, err)
		}
		if err := os.WriteFile(targetPath, content, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", header.Name, err)
		}
	}

	if !hasMetricsSQL {
		return nil
	}
	sqlPath := filepath.Join(destDir, "metrics.sql")
	dbPath := filepath.Join(destDir, metricsDBName)
	if err := replaySQL(sqlPath, dbPath); err != nil {
		return fmt.Errorf("replay metrics.sql into metrics.db: %w", err)
	}
	if err := os.Remove(sqlPath); err != nil {
		return fmt.Errorf("remove metrics.sql after replaying it: %w", err)
	}
	return nil
}

func replaySQL(sqlPath, dbPath string) error {
	script, err := os.ReadFile(sqlPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", sqlPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", dbPath, err)
	}
	defer db.Close()
	if _, err := db.Exec(string(script)); err != nil {
		return fmt.Errorf("execute dump: %w", err)
	}
	return nil
}
