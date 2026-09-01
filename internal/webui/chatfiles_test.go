package webui

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/chatfiles"
)

func newChatFilesTestServer(t *testing.T) *Server {
	t.Helper()
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	store, err := chatfiles.NewStore(filepath.Join(t.TempDir(), "chatfiles"))
	if err != nil {
		t.Fatalf("chatfiles.NewStore: %v", err)
	}
	server.SetChatFilesStore(store)
	return server
}

func uploadChatFile(t *testing.T, server *Server, sessionID, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("session_id", sessionID); err != nil {
		t.Fatalf("write session_id field: %v", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/chat/files", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestChatFilesDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/chat/files?session_id=abcdefgh", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var decoded struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Enabled {
		t.Error("expected enabled=false when no chat files store is configured")
	}
}

func TestChatFilesUploadAndList(t *testing.T) {
	server := newChatFilesTestServer(t)

	response := uploadChatFile(t, server, "abcdefgh12345678", "notes.txt", []byte("hello"))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var uploadDecoded struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &uploadDecoded); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if uploadDecoded.Name != "notes.txt" {
		t.Errorf("uploaded name = %q, want notes.txt", uploadDecoded.Name)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/chat/files?session_id=abcdefgh12345678", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}
	var listDecoded struct {
		Enabled bool `json:"enabled"`
		Files   []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listDecoded); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if !listDecoded.Enabled {
		t.Error("expected enabled=true")
	}
	if len(listDecoded.Files) != 1 || listDecoded.Files[0].Name != "notes.txt" || listDecoded.Files[0].Size != 5 {
		t.Errorf("files = %#v", listDecoded.Files)
	}
}

func TestChatFilesUploadRejectsInvalidSessionID(t *testing.T) {
	server := newChatFilesTestServer(t)
	response := uploadChatFile(t, server, "../escape", "notes.txt", []byte("x"))
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an invalid session_id", response.Code)
	}
}

func TestChatFilesDelete(t *testing.T) {
	server := newChatFilesTestServer(t)
	if response := uploadChatFile(t, server, "abcdefgh12345678", "notes.txt", []byte("hello")); response.Code != http.StatusOK {
		t.Fatalf("upload status = %d", response.Code)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/chat/files?session_id=abcdefgh12345678&name=notes.txt", nil)
	deleteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/chat/files?session_id=abcdefgh12345678", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	var listDecoded struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listDecoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listDecoded.Files) != 0 {
		t.Errorf("files = %#v, want empty after delete", listDecoded.Files)
	}
}
