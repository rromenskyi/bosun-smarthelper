package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/cameras"
)

// SetCameraManager wires in multi-camera live view + archive browsing
// (docs/cameras.md) — optional; nil (the default) means GET
// /api/cameras/list always returns an empty list, so the web UI's 📹
// button can just hide itself, the same idiom as metricsStore/
// backupS3Cfg.
func (s *Server) SetCameraManager(manager *cameras.Manager, dataDir string) {
	s.cameraManager = manager
	s.cameraDataDir = dataDir
}

type cameraInfo struct {
	Name    string `json:"name"`
	LabelRU string `json:"label_ru"`
	LabelEN string `json:"label_en"`
}

func (s *Server) handleCamerasList(w http.ResponseWriter, r *http.Request) {
	if s.cameraManager == nil {
		writeJSON(w, http.StatusOK, map[string]any{"cameras": []cameraInfo{}})
		return
	}
	list := s.cameraManager.List()
	infos := make([]cameraInfo, 0, len(list))
	for _, c := range list {
		infos = append(infos, cameraInfo{Name: c.Name, LabelRU: c.LabelRU, LabelEN: c.LabelEN})
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": infos})
}

// handleCameraStream delegates straight to the named camera's Relay —
// the relay itself owns the single upstream connection this endpoint's
// callers (a browser tab, ffmpeg's recorder) never make directly.
func (s *Server) handleCameraStream(w http.ResponseWriter, r *http.Request) {
	relay, ok := s.cameraRelay(w, r)
	if !ok {
		return
	}
	relay.ServeHTTP(w, r)
}

type cameraArchiveEntry struct {
	Name         string `json:"name"`
	SizeBytes    int64  `json:"size_bytes"`
	LastModified string `json:"last_modified"`
}

func (s *Server) handleCameraArchiveList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cameraRelay(w, r); !ok {
		return
	}
	dir := filepath.Join(s.cameraDataDir, r.PathValue("name"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No recordings yet (or recording isn't enabled for this camera)
		// — an empty list, not an error; nothing here distinguishes
		// "never recorded" from "directory doesn't exist yet."
		writeJSON(w, http.StatusOK, map[string]any{"segments": []cameraArchiveEntry{}})
		return
	}
	segments := make([]cameraArchiveEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		segments = append(segments, cameraArchiveEntry{
			Name:         e.Name(),
			SizeBytes:    info.Size(),
			LastModified: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].LastModified > segments[j].LastModified })
	writeJSON(w, http.StatusOK, map[string]any{"segments": segments})
}

// handleCameraArchiveFile serves one recorded segment. {file} is
// rejected unless it's a plain filename — no path separators, no ".." —
// so it can never resolve outside the camera's own segment directory
// regardless of what's requested. http.ServeContent (not a plain
// io.Copy) is what gives Range-request support, which browsers need to
// seek inside a <video> element.
func (s *Server) handleCameraArchiveFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cameraRelay(w, r); !ok {
		return
	}
	file := r.PathValue("file")
	if file == "" || file != filepath.Base(file) || strings.Contains(file, "..") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cameraDataDir, r.PathValue("name"), file)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, file, info.ModTime(), f)
}

// cameraRelay resolves {name} to a configured camera's Relay, writing a
// 404 (and returning ok=false) if the feature isn't wired up at all or
// the name doesn't match anything configured — shared by every handler
// above so an unknown camera name behaves identically everywhere.
func (s *Server) cameraRelay(w http.ResponseWriter, r *http.Request) (*cameras.Relay, bool) {
	if s.cameraManager == nil {
		http.NotFound(w, r)
		return nil, false
	}
	relay, ok := s.cameraManager.Relay(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	return relay, true
}
