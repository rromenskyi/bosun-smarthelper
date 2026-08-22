package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookNotifierSendsJSONPayload(t *testing.T) {
	var gotPayload webhookPayload
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	notifier := &WebhookNotifier{URL: server.URL}
	err := notifier.Notify(context.Background(), Alert{
		Source: "noaa", Severity: SeveritySevere, Title: "Severe Thunderstorm Warning", Body: "details here", At: at,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotPayload.Source != "noaa" || gotPayload.Severity != "severe" || gotPayload.Title != "Severe Thunderstorm Warning" {
		t.Errorf("payload = %+v", gotPayload)
	}
	if gotPayload.At != "2026-01-01T12:00:00Z" {
		t.Errorf("at = %q, want 2026-01-01T12:00:00Z", gotPayload.At)
	}
}

func TestWebhookNotifierReturnsErrorOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	notifier := &WebhookNotifier{URL: server.URL}
	if err := notifier.Notify(context.Background(), Alert{Title: "x"}); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}
