package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramNotifierSendsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	notifier := &TelegramNotifier{BotToken: "12345:abc", ChatID: "999", baseURL: server.URL}
	err := notifier.Notify(context.Background(), Alert{
		Source: "threshold", Severity: SeverityWarning, Title: "Disk almost full",
		Body: "disk_used_percent is 95%, threshold is 90%", At: time.Now(),
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPath != "/bot12345:abc/sendMessage" {
		t.Errorf("path = %q, want /bot<token>/sendMessage", gotPath)
	}
	if gotBody["chat_id"] != "999" {
		t.Errorf("chat_id = %q, want 999", gotBody["chat_id"])
	}
	if !strings.Contains(gotBody["text"], "Disk almost full") || !strings.Contains(gotBody["text"], "95%") {
		t.Errorf("text = %q, missing expected content", gotBody["text"])
	}
}

func TestTelegramNotifierReturnsErrorOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	notifier := &TelegramNotifier{BotToken: "bad", ChatID: "1", baseURL: server.URL}
	err := notifier.Notify(context.Background(), Alert{Title: "x", Body: "y"})
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
}
