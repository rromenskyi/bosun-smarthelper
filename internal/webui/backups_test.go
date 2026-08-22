package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/backup"
	"github.com/roman220/bosun-smarthelper/internal/settings"
)

func mustLoadSettingsStore(t *testing.T) *settings.Store {
	t.Helper()
	store, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"), settings.Data{})
	if err != nil {
		t.Fatalf("settings.Load: %v", err)
	}
	return store
}

func TestServerBackupsListWithoutConfigReportsUnconfigured(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	request := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Configured bool         `json:"configured"`
		Backups    []backupInfo `json:"backups"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Configured {
		t.Error("configured = true, want false without a wired backup config")
	}
	if len(body.Backups) != 0 {
		t.Errorf("backups = %+v, want empty", body.Backups)
	}
}

func TestServerBackupRunWithoutConfigReturnsNotImplemented(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	request := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 without a wired backup config", response.Code)
	}
}

func TestServerBackupRunUploadsAndRecordsSchedule(t *testing.T) {
	var uploadedBody []byte
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			uploadedBody = body
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer s3Server.Close()

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "memos.json"), []byte(`{"memos":{}}`), 0o600); err != nil {
		t.Fatalf("write memos.json: %v", err)
	}

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	s3cfg := backup.S3Config{Endpoint: s3Server.URL, Region: "us-east-1", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	server.SetBackupConfig(&s3cfg, dataDir)

	request := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result backupInfo
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Key == "" || result.SizeBytes == 0 {
		t.Errorf("result = %+v, want a non-empty key and size", result)
	}
	if len(uploadedBody) == 0 {
		t.Error("nothing was actually uploaded to the fake S3 server")
	}

	due, err := backup.DueForRun(dataDir, 24, time.Now())
	if err != nil {
		t.Fatalf("DueForRun: %v", err)
	}
	if due {
		t.Error("due = true right after a manual run, want false — the run should have reset the schedule")
	}
}

func TestServerBackupsListReturnsUploadedObjects(t *testing.T) {
	s3Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Contents><Key>bosun-backup-2026-01-01T00-00-00Z.tar.gz</Key><Size>100</Size><LastModified>2026-01-01T00:00:00.000Z</LastModified></Contents>
</ListBucketResult>`))
	}))
	defer s3Server.Close()

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	s3cfg := backup.S3Config{Endpoint: s3Server.URL, Region: "us-east-1", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	server.SetBackupConfig(&s3cfg, t.TempDir())

	request := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var body struct {
		Configured bool         `json:"configured"`
		Backups    []backupInfo `json:"backups"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Configured {
		t.Error("configured = false, want true")
	}
	if len(body.Backups) != 1 || body.Backups[0].Key != "bosun-backup-2026-01-01T00-00-00Z.tar.gz" {
		t.Errorf("backups = %+v", body.Backups)
	}
}

func TestServerSettingsGetReportsBackupConfigured(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	settingsStore := mustLoadSettingsStore(t)
	server.SetSettingsStore(settingsStore)
	s3cfg := backup.S3Config{Endpoint: "http://example.invalid", Bucket: "b"}
	server.SetBackupConfig(&s3cfg, t.TempDir())

	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var body struct {
		BackupConfigured bool `json:"backup_configured"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.BackupConfigured {
		t.Error("backup_configured = false, want true once SetBackupConfig has been called")
	}
}

func TestServerSettingsUpdateRejectsAutoBackupWithoutInterval(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSettingsStore(mustLoadSettingsStore(t))

	body, _ := json.Marshal(map[string]any{"backup_auto_enabled": true, "backup_interval_hours": 0})
	request := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for auto-enabled with no positive interval", response.Code)
	}
}
