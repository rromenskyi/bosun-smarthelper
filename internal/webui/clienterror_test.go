package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerClientErrorAcceptsReport(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	payload := `{"name":"TypeError","message":"Failed to fetch","context":"ask"}`
	request := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(payload))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body = %s", response.Code, response.Body.String())
	}
}

func TestServerClientErrorRejectsMalformedJSON(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/client-error", strings.NewReader(`not json`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}
