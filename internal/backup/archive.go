package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// metricsDBName is internal/metrics' own database filename (see
// metrics.DefaultPath) — BuildArchive replaces it with a portable SQL
// dump (see DumpSQL) rather than a raw copy.
const metricsDBName = "metrics.db"

// BuildArchive tar.gz's dataDir — Bosun's persistent data directory
// (memos, documents, sessions, settings, metric-merge suggestions, the
// error log, document images) — into w. Skips nothing else: this is a
// full, restorable snapshot, not a curated subset.
func BuildArchive(w io.Writer, dataDir string) error {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	metricsDBPath := filepath.Join(dataDir, metricsDBName)
	if _, err := os.Stat(metricsDBPath); err == nil {
		dump, err := DumpSQL(metricsDBPath)
		if err != nil {
			return fmt.Errorf("dump metrics database: %w", err)
		}
		if err := writeTarFile(tw, "metrics.sql", dump); err != nil {
			return err
		}
	}

	return filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if d.IsDir() {
			return nil // tar entries are per-file; directories are implicit in their names
		}
		if filepath.Base(path) == metricsDBName {
			return nil // replaced by metrics.sql above
		}
		relPath, err := filepath.Rel(dataDir, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}
		return writeTarFile(tw, relPath, content)
	})
}

func writeTarFile(tw *tar.Writer, name string, content []byte) error {
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header for %s: %w", name, err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("write tar content for %s: %w", name, err)
	}
	return nil
}
