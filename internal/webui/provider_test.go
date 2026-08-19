package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var errInvalidOverride = errors.New("provider override must be auto, local, or remote")

type fakeProviderOverrideController struct {
	mode string
	err  error
}

func (f *fakeProviderOverrideController) SetProviderOverride(mode string) error {
	if f.err != nil {
		return f.err
	}
	if mode == "" {
		mode = "auto"
	}
	f.mode = mode
	return nil
}

func (f *fakeProviderOverrideController) ProviderOverride() string {
	if f.mode == "" {
		return "auto"
	}
	return f.mode
}

func TestServerProviderOverrideDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/provider-override", strings.NewReader(`{"override":"local"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", response.Code)
	}
}

func TestServerProviderOverrideSetsMode(t *testing.T) {
	controller := &fakeProviderOverrideController{}
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetProviderOverrideController(controller)

	request := httptest.NewRequest(http.MethodPost, "/api/provider-override", strings.NewReader(`{"override":"local"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if controller.mode != "local" {
		t.Errorf("controller.mode = %q, want local", controller.mode)
	}

	statusResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusResp, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var body map[string]any
	if err := json.NewDecoder(statusResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body["provider_override"] != "local" {
		t.Errorf("status provider_override = %v, want local", body["provider_override"])
	}
}

func TestServerProviderOverrideRejectsInvalidMode(t *testing.T) {
	// A real *llm.Router rejects an unrecognized mode — verify the handler
	// surfaces that as a 400, not a 500 or a silent 200.
	controller := &fakeProviderOverrideController{err: errInvalidOverride}
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetProviderOverrideController(controller)

	request := httptest.NewRequest(http.MethodPost, "/api/provider-override", strings.NewReader(`{"override":"sideways"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func TestServerStatusOmitsProviderOverrideWhenNotConfigured(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if _, ok := body["provider_override"]; ok {
		t.Errorf("provider_override should be omitted when no controller is set, got %v", body["provider_override"])
	}
}
