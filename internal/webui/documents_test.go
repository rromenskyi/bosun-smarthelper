package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/documents"
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
}

// TestServerDocumentsListDelete covers list/delete over HTTP — upload now
// only happens via POST /api/files/upload (see filedump_test.go), so a
// record is seeded directly through the store here instead.
func TestServerDocumentsListDelete(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	server.SetDocumentStore(docStore)

	summary, err := docStore.Add(context.Background(), "Car manual", "Fuse 12 controls the headlights.", "")
	if err != nil {
		t.Fatalf("seed document: %v", err)
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

func TestServerDocumentAddPagesAndServeImage(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	dataDir := t.TempDir()
	server.SetDocumentStore(documents.NewStore(filepath.Join(dataDir, "documents.json"), nil))

	imagesDir := server.documents.ImagesDir()
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatalf("create images dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "fuse-panel.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("write fake image: %v", err)
	}

	payload := `{"documents":[{"title":"Fuse diagrams","pages":[{"text":"Fuse panel diagram","image_url":"/document-images/fuse-panel.png"}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/documents/pages", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("add pages status = %d, body = %s", response.Code, response.Body.String())
	}

	imageRequest := httptest.NewRequest(http.MethodGet, "/document-images/fuse-panel.png", nil)
	imageResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(imageResponse, imageRequest)
	if imageResponse.Code != http.StatusOK {
		t.Fatalf("image status = %d", imageResponse.Code)
	}
	if imageResponse.Body.String() != "fake-png-bytes" {
		t.Errorf("image body = %q", imageResponse.Body.String())
	}
}

// TestServerDocumentAddPagesAcceptsABatch guards the endpoint's actual
// reason to exist post-batching: several documents in one request must
// all land, and the response must report all of them (not just the last
// or first one).
func TestServerDocumentAddPagesAcceptsABatch(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetDocumentStore(documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil))

	payload := `{"documents":[
		{"title":"Chapter One","pages":[{"text":"first chapter content"}]},
		{"title":"Chapter Two","pages":[{"text":"second chapter content"},{"text":"more content"}]}
	]}`
	request := httptest.NewRequest(http.MethodPost, "/api/documents/pages", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("add pages status = %d, body = %s", response.Code, response.Body.String())
	}

	var decoded struct {
		Documents []struct {
			Title      string `json:"title"`
			ChunkCount int    `json:"chunk_count"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Documents) != 2 {
		t.Fatalf("response documents = %d, want 2: %s", len(decoded.Documents), response.Body.String())
	}
	if decoded.Documents[0].Title != "Chapter One" || decoded.Documents[0].ChunkCount != 1 {
		t.Errorf("documents[0] = %#v", decoded.Documents[0])
	}
	if decoded.Documents[1].Title != "Chapter Two" || decoded.Documents[1].ChunkCount != 2 {
		t.Errorf("documents[1] = %#v", decoded.Documents[1])
	}

	list, err := server.documents.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("store has %d documents, want 2", len(list))
	}
}

func TestServerDocumentAddPagesRejectsEmptyDocumentsList(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetDocumentStore(documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil))

	request := httptest.NewRequest(http.MethodPost, "/api/documents/pages", strings.NewReader(`{"documents":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an empty documents list", response.Code)
	}
}
