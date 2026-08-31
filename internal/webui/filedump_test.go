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

	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/filedump"
)

func newFileDumpTestServer(t *testing.T) (*Server, *documents.Store) {
	t.Helper()
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	store, err := filedump.NewStore(filepath.Join(t.TempDir(), "filedump"))
	if err != nil {
		t.Fatalf("filedump.NewStore: %v", err)
	}
	server.SetFileDumpStore(store)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	server.SetDocumentStore(docStore)
	return server, docStore
}

func uploadFile(t *testing.T, server *Server, fields map[string]string, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %q: %v", key, err)
		}
	}
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write file content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestFileDumpDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d", listResponse.Code)
	}
	var listBody map[string]any
	if err := json.NewDecoder(listResponse.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listBody["enabled"] != false {
		t.Errorf("enabled = %v, want false", listBody["enabled"])
	}

	folderRequest := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"","name":"x"}`)))
	folderResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(folderResponse, folderRequest)
	if folderResponse.Code != http.StatusNotImplemented {
		t.Errorf("folder status = %d, want 501", folderResponse.Code)
	}

	uploadResponse := uploadFile(t, server, nil, "a.txt", []byte("hi"))
	if uploadResponse.Code != http.StatusNotImplemented {
		t.Errorf("upload status = %d, want 501", uploadResponse.Code)
	}
}

func TestFileDumpCreateFolderAndList(t *testing.T) {
	server, _ := newFileDumpTestServer(t)

	folderRequest := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"","name":"docs"}`)))
	folderResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(folderResponse, folderRequest)
	if folderResponse.Code != http.StatusOK {
		t.Fatalf("create folder status = %d, body = %s", folderResponse.Code, folderResponse.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	var listBody map[string]any
	if err := json.NewDecoder(listResponse.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listBody["enabled"] != true {
		t.Errorf("enabled = %v, want true", listBody["enabled"])
	}
	folders := listBody["folders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("folders = %#v, want 1 entry", folders)
	}
}

func TestFileDumpUploadWithoutRAG(t *testing.T) {
	server, docStore := newFileDumpTestServer(t)

	response := uploadFile(t, server, map[string]string{"path": ""}, "manual.txt", []byte("Fuse 12 controls the headlights."))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["path"] != "manual.txt" {
		t.Errorf("path = %v, want manual.txt", body["path"])
	}
	if body["in_rag"] != false {
		t.Errorf("in_rag = %v, want false", body["in_rag"])
	}

	docs, err := docStore.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("documents = %#v, want none for a non-RAG upload", docs)
	}

	fileRequest := httptest.NewRequest(http.MethodGet, "/files/manual.txt", nil)
	fileResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(fileResponse, fileRequest)
	if fileResponse.Code != http.StatusOK {
		t.Fatalf("download status = %d", fileResponse.Code)
	}
	if fileResponse.Body.String() != "Fuse 12 controls the headlights." {
		t.Errorf("download body = %q", fileResponse.Body.String())
	}
}

func TestFileDumpUploadWithRAGTaggedBySourcePath(t *testing.T) {
	server, docStore := newFileDumpTestServer(t)

	folderRequest := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"","name":"docs"}`)))
	server.Handler().ServeHTTP(httptest.NewRecorder(), folderRequest)
	folderRequest2 := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"docs","name":"ford"}`)))
	server.Handler().ServeHTTP(httptest.NewRecorder(), folderRequest2)

	response := uploadFile(t, server, map[string]string{
		"path":       "docs/ford",
		"add_to_rag": "true",
		"title":      "Generator repair",
	}, "manual.txt", []byte("Fuse 12 controls the headlights."))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["in_rag"] != true {
		t.Fatalf("in_rag = %v, want true, body = %#v", body["in_rag"], body)
	}
	documentID, _ := body["document_id"].(string)
	if documentID == "" {
		t.Fatalf("expected document_id in response, got %#v", body)
	}

	docs, err := docStore.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != documentID || docs[0].Title != "Generator repair" {
		t.Fatalf("documents = %#v", docs)
	}

	results, err := docStore.Search(t.Context(), "headlights", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].SourcePath != "docs/ford" {
		t.Fatalf("results = %#v, want source_path docs/ford", results)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/files?path=docs/ford", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	var listBody map[string]any
	if err := json.NewDecoder(listResponse.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	files := listBody["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %#v, want 1 entry", files)
	}
	fileEntry := files[0].(map[string]any)
	if fileEntry["in_rag"] != true || fileEntry["document_id"] != documentID {
		t.Errorf("file entry = %#v", fileEntry)
	}
}

func TestFileDumpUploadWithRAGIngestsStandaloneImage(t *testing.T) {
	server, docStore := newFileDumpTestServer(t)

	folderRequest := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"","name":"ford-e350"}`)))
	server.Handler().ServeHTTP(httptest.NewRecorder(), folderRequest)

	response := uploadFile(t, server, map[string]string{
		"path":       "ford-e350",
		"add_to_rag": "true",
		"title":      "Fuse panel diagram",
	}, "fuse-panel.png", onePixelPNG)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["in_rag"] != true {
		t.Fatalf("in_rag = %v, want true, body = %#v", body["in_rag"], body)
	}
	documentID, _ := body["document_id"].(string)
	if documentID == "" {
		t.Fatalf("expected document_id in response, got %#v", body)
	}

	docs, err := docStore.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != documentID || docs[0].Title != "Fuse panel diagram" || docs[0].SourcePath != "ford-e350" {
		t.Fatalf("documents = %#v", docs)
	}
}

func TestFileDumpUploadRAGFailureDoesNotBlockRawUpload(t *testing.T) {
	server, docStore := newFileDumpTestServer(t)

	// %PDF header plus invalid UTF-8 — not a real PDF and not valid text,
	// so ingestion fails, but the raw file must still be saved.
	response := uploadFile(t, server, map[string]string{"add_to_rag": "true"}, "manual.pdf", []byte("%PDF-1.4\n\xff\xfe\x00binary"))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["in_rag"] != false {
		t.Errorf("in_rag = %v, want false", body["in_rag"])
	}
	if _, ok := body["rag_warning"]; !ok {
		t.Errorf("expected a rag_warning, got %#v", body)
	}

	fileRequest := httptest.NewRequest(http.MethodGet, "/files/manual.pdf", nil)
	fileResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(fileResponse, fileRequest)
	if fileResponse.Code != http.StatusOK {
		t.Fatalf("expected the raw file to still be saved, download status = %d", fileResponse.Code)
	}

	docs, err := docStore.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("documents = %#v, want none after a failed ingestion", docs)
	}
}

func TestFileDumpUploadRequiresFile(t *testing.T) {
	server, _ := newFileDumpTestServer(t)
	response := uploadFile(t, server, map[string]string{"path": ""}, "", nil)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func TestFileDumpMoveUpdatesDocumentSourcePath(t *testing.T) {
	server, docStore := newFileDumpTestServer(t)

	uploadResponse := uploadFile(t, server, map[string]string{"add_to_rag": "true"}, "manual.txt", []byte("Fuse 12 controls the headlights."))
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	var uploadBody map[string]any
	if err := json.NewDecoder(uploadResponse.Body).Decode(&uploadBody); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	documentID := uploadBody["document_id"].(string)

	folderRequest := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"","name":"archive"}`)))
	server.Handler().ServeHTTP(httptest.NewRecorder(), folderRequest)

	moveRequest := httptest.NewRequest(http.MethodPost, "/api/files/move", bytes.NewReader([]byte(`{"from":"manual.txt","to":"archive/manual.txt"}`)))
	moveResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(moveResponse, moveRequest)
	if moveResponse.Code != http.StatusOK {
		t.Fatalf("move status = %d, body = %s", moveResponse.Code, moveResponse.Body.String())
	}

	results, err := docStore.Search(t.Context(), "headlights", 5, documentID)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].SourcePath != "archive" {
		t.Fatalf("results = %#v, want source_path archive after the move", results)
	}
}

func TestFileDumpDeleteCascadesDocument(t *testing.T) {
	server, docStore := newFileDumpTestServer(t)

	uploadResponse := uploadFile(t, server, map[string]string{"add_to_rag": "true"}, "manual.txt", []byte("Fuse 12 controls the headlights."))
	var uploadBody map[string]any
	if err := json.NewDecoder(uploadResponse.Body).Decode(&uploadBody); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	documentID := uploadBody["document_id"].(string)

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/files?path=manual.txt", nil)
	deleteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var deleteBody map[string]any
	if err := json.NewDecoder(deleteResponse.Body).Decode(&deleteBody); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	deletedIDs := deleteBody["deleted_document_ids"].([]any)
	if len(deletedIDs) != 1 || deletedIDs[0] != documentID {
		t.Fatalf("deleted_document_ids = %#v", deletedIDs)
	}

	docs, err := docStore.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("documents = %#v, want none after cascade delete", docs)
	}

	fileRequest := httptest.NewRequest(http.MethodGet, "/files/manual.txt", nil)
	fileResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(fileResponse, fileRequest)
	if fileResponse.Code == http.StatusOK {
		t.Error("expected the raw file to be gone after delete")
	}
}

func TestFileDumpDeleteNonEmptyFolderRequiresRecursive(t *testing.T) {
	server, _ := newFileDumpTestServer(t)

	folderRequest := httptest.NewRequest(http.MethodPost, "/api/files/folder", bytes.NewReader([]byte(`{"path":"","name":"docs"}`)))
	server.Handler().ServeHTTP(httptest.NewRecorder(), folderRequest)
	uploadResponse := uploadFile(t, server, map[string]string{"path": "docs"}, "manual.txt", []byte("text"))
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", uploadResponse.Code, uploadResponse.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/files?path=docs", nil)
	deleteResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without recursive=true", deleteResponse.Code)
	}

	recursiveRequest := httptest.NewRequest(http.MethodDelete, "/api/files?path=docs&recursive=true", nil)
	recursiveResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(recursiveResponse, recursiveRequest)
	if recursiveResponse.Code != http.StatusOK {
		t.Fatalf("recursive delete status = %d, body = %s", recursiveResponse.Code, recursiveResponse.Body.String())
	}
}
