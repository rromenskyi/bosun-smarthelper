package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerFeedbackAcceptsUpAndDown(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	for _, rating := range []string{"up", "down"} {
		payload := `{"text":"Капитан! Всё в норме.","rating":"` + rating + `"}`
		request := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(payload))
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("rating %q: status = %d, want 200", rating, response.Code)
		}
	}
}

func TestServerFeedbackRejectsInvalidRating(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(`{"text":"hi","rating":"sideways"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func TestServerFeedbackRejectsMalformedJSON(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(`not json`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}
