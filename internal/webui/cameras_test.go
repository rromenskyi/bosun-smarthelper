package webui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/cameras"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestHandleCamerasListEmptyWhenUnconfigured(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/cameras/list", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	var body struct {
		Cameras []cameraInfo `json:"cameras"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Cameras) != 0 {
		t.Errorf("cameras = %+v, want empty when no manager is wired up", body.Cameras)
	}
}

func TestHandleCamerasListReturnsConfigured(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	manager := cameras.NewManager([]cameras.Config{
		{Name: "front", LabelRU: "Нос", LabelEN: "Bow", StreamURL: "http://unused.invalid"},
	}, discardLogger())
	server.SetCameraManager(manager, t.TempDir())

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/list", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	var body struct {
		Cameras []cameraInfo `json:"cameras"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Cameras) != 1 || body.Cameras[0].Name != "front" || body.Cameras[0].LabelRU != "Нос" {
		t.Errorf("cameras = %+v", body.Cameras)
	}
}

func TestHandleCameraStreamUnknownCameraReturns404(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	manager := cameras.NewManager([]cameras.Config{{Name: "front", StreamURL: "http://unused.invalid"}}, discardLogger())
	server.SetCameraManager(manager, t.TempDir())

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/does-not-exist/stream", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.Code)
	}
}

func TestHandleCameraStreamWithoutManagerReturns404(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/cameras/front/stream", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.Code)
	}
}

func TestHandleCameraArchiveListReturnsFilesSortedNewestFirst(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	manager := cameras.NewManager([]cameras.Config{{Name: "front", StreamURL: "http://unused.invalid"}}, discardLogger())
	dataDir := t.TempDir()
	server.SetCameraManager(manager, dataDir)

	camDir := filepath.Join(dataDir, "front")
	if err := os.MkdirAll(camDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	older := filepath.Join(camDir, "cam_000.mp4")
	newer := filepath.Join(camDir, "cam_001.mp4")
	if err := os.WriteFile(older, []byte("old"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.WriteFile(newer, []byte("new-longer"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/front/archive", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	var body struct {
		Segments []cameraArchiveEntry `json:"segments"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Segments) != 2 || body.Segments[0].Name != "cam_001.mp4" {
		t.Errorf("segments = %+v, want cam_001.mp4 (newer) first", body.Segments)
	}
}

func TestHandleCameraArchiveListEmptyWhenNoRecordingsYet(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	manager := cameras.NewManager([]cameras.Config{{Name: "front", StreamURL: "http://unused.invalid"}}, discardLogger())
	server.SetCameraManager(manager, t.TempDir())

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/front/archive", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty list, not an error)", response.Code)
	}
	var body struct {
		Segments []cameraArchiveEntry `json:"segments"`
	}
	json.NewDecoder(response.Body).Decode(&body)
	if len(body.Segments) != 0 {
		t.Errorf("segments = %+v, want empty", body.Segments)
	}
}

func TestHandleCameraArchiveFileServesTheRealFile(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	manager := cameras.NewManager([]cameras.Config{{Name: "front", StreamURL: "http://unused.invalid"}}, discardLogger())
	dataDir := t.TempDir()
	server.SetCameraManager(manager, dataDir)
	camDir := filepath.Join(dataDir, "front")
	os.MkdirAll(camDir, 0o755)
	os.WriteFile(filepath.Join(camDir, "cam_000.mp4"), []byte("video-bytes"), 0o644)

	request := httptest.NewRequest(http.MethodGet, "/api/cameras/front/archive/cam_000.mp4", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Body.String() != "video-bytes" {
		t.Errorf("body = %q", response.Body.String())
	}
}

func TestHandleCameraArchiveFileRejectsPathTraversal(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	manager := cameras.NewManager([]cameras.Config{{Name: "front", StreamURL: "http://unused.invalid"}}, discardLogger())
	dataDir := t.TempDir()
	server.SetCameraManager(manager, dataDir)
	// A real secret file one level up from the camera's own segment dir —
	// the request below must never be able to reach it.
	os.MkdirAll(filepath.Join(dataDir, "front"), 0o755)
	os.WriteFile(filepath.Join(dataDir, "secret.txt"), []byte("do-not-leak"), 0o644)

	for _, file := range []string{"..%2Fsecret.txt", "..", "%2e%2e%2fsecret.txt"} {
		request := httptest.NewRequest(http.MethodGet, "/api/cameras/front/archive/"+file, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code == http.StatusOK && response.Body.String() == "do-not-leak" {
			t.Errorf("file=%q leaked the secret file outside the camera's own directory", file)
		}
	}
}
