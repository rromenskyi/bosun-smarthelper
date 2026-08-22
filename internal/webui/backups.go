package webui

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/backup"
)

// SetBackupConfig wires in `smarthelper backup`'s destination (config.yaml's
// backup.s3, resolved to real credentials) so the web UI's settings page
// can list existing backups and trigger one on demand — see docs/backup.md
// and docs/settings.md. Optional: nil s3cfg (the default, when backup.s3
// isn't configured) means the settings page reports the feature as
// unconfigured rather than erroring.
func (s *Server) SetBackupConfig(s3cfg *backup.S3Config, dataDir string) {
	s.backupS3Cfg = s3cfg
	s.backupDataDir = dataDir
}

// backupInfo is one listed backup, JSON-shaped for the settings page.
type backupInfo struct {
	Key          string `json:"key"`
	SizeBytes    int64  `json:"size_bytes"`
	LastModified string `json:"last_modified"`
}

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	if s.backupS3Cfg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "backups": []backupInfo{}})
		return
	}
	objects, err := backup.ListObjects(r.Context(), *s.backupS3Cfg, "bosun-backup-")
	if err != nil {
		s.logger.Error("list backups", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list backups"})
		return
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].LastModified.After(objects[j].LastModified) })
	infos := make([]backupInfo, 0, len(objects))
	for _, o := range objects {
		infos = append(infos, backupInfo{Key: o.Key, SizeBytes: o.Size, LastModified: o.LastModified.Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "backups": infos})
}

// handleBackupRun triggers one backup immediately — the web UI's "back up
// now" button, doing exactly what `smarthelper backup` does and recording
// the run the same way an automatic scheduled run would (see
// internal/backup.RecordRun), so a manual click also resets the countdown
// to the next automatic one.
func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	if s.backupS3Cfg == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "backup is not configured"})
		return
	}
	var archive bytes.Buffer
	if err := backup.BuildArchive(&archive, s.backupDataDir); err != nil {
		s.logger.Error("build backup archive", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build archive"})
		return
	}
	key := fmt.Sprintf("bosun-backup-%s.tar.gz", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
	if err := backup.PutObject(r.Context(), *s.backupS3Cfg, key, archive.Bytes(), "application/gzip"); err != nil {
		s.logger.Error("upload backup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}
	now := time.Now()
	if err := backup.RecordRun(s.backupDataDir, now); err != nil {
		s.logger.Warn("record backup run", "error", err)
	}
	writeJSON(w, http.StatusOK, backupInfo{Key: key, SizeBytes: int64(archive.Len()), LastModified: now.UTC().Format(time.RFC3339)})
}
