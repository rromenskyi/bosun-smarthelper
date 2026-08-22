package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/tools"
)

func TestServerQuickToolWithoutRegistryReturnsNotImplemented(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	request := httptest.NewRequest(http.MethodGet, "/api/quick/get_system_info", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 without a wired registry", response.Code)
	}
}

func TestServerQuickToolRejectsUnlistedTool(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	registry := tools.NewRegistry()
	registry.Register(tools.NewWebSearchTool(&config.OnlineConfig{}))
	server.SetToolRegistry(registry)

	request := httptest.NewRequest(http.MethodGet, "/api/quick/web_search", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a tool outside the quick-access allowlist", response.Code)
	}
}

func TestServerQuickToolSystemInfoFormatsPlainAnswer(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	registry := tools.NewRegistry()
	registry.Register(tools.NewSystemTool(&config.SystemConfig{}))
	server.SetToolRegistry(registry)

	request := httptest.NewRequest(http.MethodGet, "/api/quick/get_system_info?lang=en", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Answer == "" {
		t.Error("answer is empty")
	}
	for _, want := range []string{"Uptime:", "CPU:", "Memory:", "Disk:"} {
		if !strings.Contains(body.Answer, want) {
			t.Errorf("answer = %q, want it to contain %q", body.Answer, want)
		}
	}
}

func TestServerQuickToolGPSUsesMockReading(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	registry := tools.NewRegistry()
	registry.Register(tools.NewGPSTool(&config.GPSConfig{
		Type: "mock", MockLatitude: 40.7608, MockLongitude: -111.8910, MockSpeedKMH: 12, MockAltitudeM: 1300,
	}))
	server.SetToolRegistry(registry)

	request := httptest.NewRequest(http.MethodGet, "/api/quick/get_gps", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, want := range []string{"40.76080", "-111.89100", "12"} {
		if !strings.Contains(body.Answer, want) {
			t.Errorf("answer = %q, want it to contain %q", body.Answer, want)
		}
	}
}

func TestServerQuickToolErrorStillReturns200WithAMessage(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	registry := tools.NewRegistry()
	registry.Register(tools.NewGPSTool(&config.GPSConfig{Type: "unsupported-backend"}))
	server.SetToolRegistry(registry)

	request := httptest.NewRequest(http.MethodGet, "/api/quick/get_gps", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the tool itself errors (so the chat UI can show it as a normal reply)", response.Code)
	}
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Answer == "" {
		t.Error("answer is empty, want an error explanation")
	}
}
