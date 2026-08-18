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

	"github.com/roman220/ai-local-smarthelper/internal/documents"
)

func TestServerDocumentsDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	request := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d", response.Code)
	}
	var listBody map[string]any
	if err := json.NewDecoder(response.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listBody["enabled"] != false {
		t.Errorf("enabled = %v, want false", listBody["enabled"])
	}

	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/documents", nil)
	uploadResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusNotImplemented {
		t.Errorf("upload status = %d, want 501", uploadResponse.Code)
	}
}

func TestServerDocumentsUploadListDelete(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetDocumentStore(documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("title", "Car manual"); err != nil {
		t.Fatalf("write title field: %v", err)
	}
	part, err := writer.CreateFormFile("file", "manual.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("Fuse 12 controls the headlights.")); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/documents", &body)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	var summary documents.Summary
	if err := json.NewDecoder(uploadResponse.Body).Decode(&summary); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if summary.Title != "Car manual" || summary.ChunkCount != 1 {
		t.Errorf("summary = %#v", summary)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/documents", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	var listBody map[string]any
	if err := json.NewDecoder(listResponse.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listBody["enabled"] != true {
		t.Errorf("enabled = %v, want true", listBody["enabled"])
	}
	docs := listBody["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("documents = %#v, want 1 entry", docs)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/documents/"+summary.ID, nil)
	deleteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}

	deleteAgainResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteAgainResponse, httptest.NewRequest(http.MethodDelete, "/api/documents/"+summary.ID, nil))
	if deleteAgainResponse.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", deleteAgainResponse.Code)
	}
}

func TestServerDocumentUploadRequiresFile(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetDocumentStore(documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("title", "No file")
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/documents", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}
