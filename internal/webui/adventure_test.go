package webui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/adventure"
)

func newAdventureTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := adventure.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("adventure.Open failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAdventureStore(store)
	return server
}

func doRequest(server *Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestHandleAdventureSessionsUnavailableWithoutStore(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	response := doRequest(server, http.MethodGet, "/api/adventure/sessions", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleAdventureSessionCreateAndList(t *testing.T) {
	server := newAdventureTestServer(t)

	response := doRequest(server, http.MethodPost, "/api/adventure/sessions", `{"name":"quest1"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}

	response = doRequest(server, http.MethodGet, "/api/adventure/sessions", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"quest1"`) {
		t.Errorf("expected the created session in the list, got %s", response.Body.String())
	}
}

func TestHandleAdventureSessionCreateDuplicateNameConflicts(t *testing.T) {
	server := newAdventureTestServer(t)

	doRequest(server, http.MethodPost, "/api/adventure/sessions", `{"name":"quest1"}`)
	response := doRequest(server, http.MethodPost, "/api/adventure/sessions", `{"name":"quest1"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestHandleAdventureSessionRename(t *testing.T) {
	server := newAdventureTestServer(t)
	doRequest(server, http.MethodPost, "/api/adventure/sessions", `{"name":"quest1"}`)

	response := doRequest(server, http.MethodPatch, "/api/adventure/sessions/quest1", `{"new_name":"quest2"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	response = doRequest(server, http.MethodGet, "/api/adventure/sessions", "")
	if !strings.Contains(response.Body.String(), `"quest2"`) || strings.Contains(response.Body.String(), `"quest1"`) {
		t.Errorf("rename did not take effect: %s", response.Body.String())
	}
}

func TestHandleAdventureSessionRenameNotFound(t *testing.T) {
	server := newAdventureTestServer(t)

	response := doRequest(server, http.MethodPatch, "/api/adventure/sessions/nope", `{"new_name":"quest2"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestHandleAdventureSessionDelete(t *testing.T) {
	server := newAdventureTestServer(t)
	doRequest(server, http.MethodPost, "/api/adventure/sessions", `{"name":"quest1"}`)

	response := doRequest(server, http.MethodDelete, "/api/adventure/sessions/quest1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	response = doRequest(server, http.MethodGet, "/api/adventure/sessions", "")
	if strings.Contains(response.Body.String(), `"quest1"`) {
		t.Errorf("expected quest1 to be gone: %s", response.Body.String())
	}
}

func TestHandleAdventureModeRequiresExistingSession(t *testing.T) {
	server := newAdventureTestServer(t)

	response := doRequest(server, http.MethodPost, "/api/adventure/mode",
		`{"session_id":"abcdefgh12345678","enabled":true,"adventure_session":"nope"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestHandleAdventureModeEnableAndDisable(t *testing.T) {
	server := newAdventureTestServer(t)
	doRequest(server, http.MethodPost, "/api/adventure/sessions", `{"name":"quest1"}`)

	response := doRequest(server, http.MethodPost, "/api/adventure/mode",
		`{"session_id":"abcdefgh12345678","enabled":true,"adventure_session":"quest1"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", response.Code, response.Body.String())
	}

	response = doRequest(server, http.MethodPost, "/api/adventure/mode",
		`{"session_id":"abcdefgh12345678","enabled":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHandleChatRoutesToGameModeWithoutLLM(t *testing.T) {
	asker := &fakeAsker{answer: "the LLM should never be called"}
	store, err := adventure.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("adventure.Open failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.CreateSession("quest1", 42); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	server := NewServer(asker, nil, time.Second, "ru", nil)
	server.SetAdventureStore(store)

	sessionID := "abcdefgh12345678"
	response := doRequest(server, http.MethodPost, "/api/adventure/mode",
		`{"session_id":"`+sessionID+`","enabled":true,"adventure_session":"quest1"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("mode status = %d, body = %s", response.Code, response.Body.String())
	}

	response = doRequest(server, http.MethodPost, "/api/chat",
		`{"message":"look","session_id":"`+sessionID+`","language":"en"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "the LLM should never be called") {
		t.Error("game mode turn reached the LLM asker instead of the game engine")
	}
	if asker.seen != "" {
		t.Errorf("asker.Ask should never have been called, but saw message %q", asker.seen)
	}
}
