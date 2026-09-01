package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/notifications"
)

func newNotificationsTestServer(t *testing.T) (*Server, *notifications.Store) {
	t.Helper()
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	store := notifications.NewStore(filepath.Join(t.TempDir(), "notifications.json"))
	server.SetNotificationsStore(store)
	return server, store
}

func TestNotificationsDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
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
		t.Error("expected enabled=false when no notifications store is configured")
	}
}

func TestNotificationsList(t *testing.T) {
	server, store := newNotificationsTestServer(t)
	if _, err := store.Add(notifications.Notification{Source: "threshold", Title: "Battery low", Body: "12.1V"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Enabled       bool `json:"enabled"`
		UnreadCount   int  `json:"unread_count"`
		Notifications []struct {
			Title string `json:"title"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.Enabled || decoded.UnreadCount != 1 || len(decoded.Notifications) != 1 || decoded.Notifications[0].Title != "Battery low" {
		t.Errorf("decoded = %#v", decoded)
	}
}

func TestNotificationsMarkReadOne(t *testing.T) {
	server, store := newNotificationsTestServer(t)
	n, err := store.Add(notifications.Notification{Title: "A"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"id": n.ID})
	request := httptest.NewRequest(http.MethodPost, "/api/notifications/read", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	count, err := store.UnreadCount()
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if count != 0 {
		t.Errorf("UnreadCount = %d, want 0", count)
	}
}

func TestNotificationsMarkReadAll(t *testing.T) {
	server, store := newNotificationsTestServer(t)
	if _, err := store.Add(notifications.Notification{Title: "A"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Add(notifications.Notification{Title: "B"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	body, _ := json.Marshal(map[string]bool{"all": true})
	request := httptest.NewRequest(http.MethodPost, "/api/notifications/read", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	count, err := store.UnreadCount()
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if count != 0 {
		t.Errorf("UnreadCount = %d, want 0", count)
	}
}

func TestNotificationsDelete(t *testing.T) {
	server, store := newNotificationsTestServer(t)
	n, err := store.Add(notifications.Notification{Title: "A"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/notifications?id="+n.ID, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %#v, want empty after delete", list)
	}
}
