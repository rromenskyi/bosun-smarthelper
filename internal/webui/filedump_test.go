package webui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/filedump"
	"github.com/roman220/bosun-smarthelper/internal/notifications"
)

// onePixelPNG is a minimal valid 1x1 transparent PNG — enough for the RAG
// ingestion path to recognize it as a real image without needing a real
// diagram scan. Same fixture internal/documents/ingest_test.go uses; kept
// as a separate copy here rather than exported across packages, since it's
// test-only data with no other reason to be part of documents' public API.
var onePixelPNG = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

func mustDecodeBase64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return data
}

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

// waitForFileIngestion polls GET /api/files?path=dir until name's
// background RAG ingestion (see ingestFileDumpUploadAsync) finishes —
// rag_pending no longer true — or 2 seconds pass, then returns that
// file's listing entry either way. Ingestion moved to a background
// goroutine so a slow OCR-heavy upload's response isn't held open long
// enough to hit an intermediate proxy's own timeout; tests observe the
// result the same way a real client now has to, by polling the listing,
// not from the upload response itself.
func waitForFileIngestion(t *testing.T, server *Server, dir, name string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		request := httptest.NewRequest(http.MethodGet, "/api/files?path="+dir, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		files, _ := body["files"].([]any)
		for _, f := range files {
			entry, _ := f.(map[string]any)
			if entry["name"] == name && entry["rag_pending"] != true {
				return entry
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s/%s ingestion to finish", dir, name)
		}
		time.Sleep(5 * time.Millisecond)
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
	if body["rag_pending"] != true {
		t.Fatalf("rag_pending = %v, want true — ingestion now runs in the background", body["rag_pending"])
	}

	fileEntry := waitForFileIngestion(t, server, "docs/ford", "manual.txt")
	if fileEntry["in_rag"] != true {
		t.Fatalf("file entry = %#v, want in_rag true once ingestion finishes", fileEntry)
	}
	documentID, _ := fileEntry["document_id"].(string)
	if documentID == "" {
		t.Fatalf("expected document_id in file entry, got %#v", fileEntry)
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

	fileEntry := waitForFileIngestion(t, server, "ford-e350", "fuse-panel.png")
	if fileEntry["in_rag"] != true {
		t.Fatalf("file entry = %#v, want in_rag true once ingestion finishes", fileEntry)
	}
	documentID, _ := fileEntry["document_id"].(string)
	if documentID == "" {
		t.Fatalf("expected document_id in file entry, got %#v", fileEntry)
	}

	docs, err := docStore.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != documentID || docs[0].Title != "Fuse panel diagram" || docs[0].SourcePath != "ford-e350" {
		t.Fatalf("documents = %#v", docs)
	}
}

// TestFileDumpUploadWithRAGRecordsSuccessNotification guards the
// notification zone's one bit of visibility a background upload has
// beyond a badge in the file browser: a completed ingestion — success or
// failure — must show up in internal/notifications.
func TestFileDumpUploadWithRAGRecordsSuccessNotification(t *testing.T) {
	server, _ := newFileDumpTestServer(t)
	notificationsStore := notifications.NewStore(filepath.Join(t.TempDir(), "notifications.json"))
	server.SetNotificationsStore(notificationsStore)

	response := uploadFile(t, server, map[string]string{"add_to_rag": "true"}, "notes.txt", []byte("fuse panel wiring notes"))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	waitForFileIngestion(t, server, "", "notes.txt")

	list, err := notificationsStore.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Source != "filedump" || list[0].Severity != "info" || list[0].Title != "notes.txt" {
		t.Fatalf("notifications = %#v, want one info notification for notes.txt", list)
	}
}

func TestFileDumpUploadWithRAGRecordsFailureNotification(t *testing.T) {
	server, _ := newFileDumpTestServer(t)
	notificationsStore := notifications.NewStore(filepath.Join(t.TempDir(), "notifications.json"))
	server.SetNotificationsStore(notificationsStore)

	response := uploadFile(t, server, map[string]string{"add_to_rag": "true"}, "manual.pdf", []byte("%PDF-1.4\n\xff\xfe\x00binary"))
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	waitForFileIngestion(t, server, "", "manual.pdf")

	list, err := notificationsStore.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Source != "filedump" || list[0].Severity != "warning" || list[0].Title != "manual.pdf" {
		t.Fatalf("notifications = %#v, want one warning notification for manual.pdf", list)
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
	if body["rag_pending"] != true {
		t.Fatalf("rag_pending = %v, want true — ingestion now runs in the background", body["rag_pending"])
	}

	fileEntry := waitForFileIngestion(t, server, "", "manual.pdf")
	if fileEntry["in_rag"] != nil && fileEntry["in_rag"] != false {
		t.Errorf("file entry in_rag = %v, want false/absent", fileEntry["in_rag"])
	}
	if fileEntry["rag_error"] == nil || fileEntry["rag_error"] == "" {
		t.Errorf("expected a rag_error once ingestion finishes, got %#v", fileEntry)
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
	fileEntry := waitForFileIngestion(t, server, "", "manual.txt")
	documentID, _ := fileEntry["document_id"].(string)
	if documentID == "" {
		t.Fatalf("expected document_id once ingestion finishes, got %#v", fileEntry)
	}

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
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	fileEntry := waitForFileIngestion(t, server, "", "manual.txt")
	documentID, _ := fileEntry["document_id"].(string)
	if documentID == "" {
		t.Fatalf("expected document_id once ingestion finishes, got %#v", fileEntry)
	}

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
