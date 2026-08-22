package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/settings"
)

// personaFakeAsker implements both Asker and personaSetter, so
// handleSettingsUpdate can be observed applying persona changes live.
type personaFakeAsker struct {
	nameRU, nameEN, stylePrompt string
}

func (f *personaFakeAsker) Ask(_ context.Context, _ string) (string, error) { return "", nil }

func (f *personaFakeAsker) SetPersona(nameRU, nameEN, stylePrompt string) {
	if nameRU != "" {
		f.nameRU = nameRU
	}
	if nameEN != "" {
		f.nameEN = nameEN
	}
	f.stylePrompt = stylePrompt
}

type settingsResponse struct {
	Enabled         bool          `json:"enabled"`
	Settings        settings.Data `json:"settings"`
	CACertAvailable bool          `json:"ca_cert_available"`
}

func TestServerSettingsDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["enabled"] != false {
		t.Errorf("enabled = %v, want false", body["enabled"])
	}
}

func TestServerSettingsGetAndUpdate(t *testing.T) {
	asker := &personaFakeAsker{}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	storePath := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.Load(storePath, settings.Data{
		NameRU: "Старпом", StylePrompt: "old prompt", DefaultLanguage: "ru",
		RemoteTemperature: 0.8, LocalTemperature: 0.5,
	})
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	server.SetSettingsStore(store)

	getResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResp, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d", getResp.Code)
	}
	var getBody settingsResponse
	if err := json.NewDecoder(getResp.Body).Decode(&getBody); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if !getBody.Enabled || getBody.Settings.StylePrompt != "old prompt" {
		t.Fatalf("GET body = %#v", getBody)
	}

	payload := `{"name_ru":"Капитан","style_prompt":"new prompt","default_language":"en","remote_temperature":0.3,"local_temperature":0.2}`
	postReq := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(payload))
	postReq.Header.Set("Content-Type", "application/json")
	postResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(postResp, postReq)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST status = %d body = %s", postResp.Code, postResp.Body.String())
	}

	// Applied live to the asker (personaSetter).
	if asker.nameRU != "Капитан" || asker.stylePrompt != "new prompt" {
		t.Errorf("persona not applied live: %#v", asker)
	}
	// Applied live to the default language.
	if got := server.getDefaultLanguage(); got != "en" {
		t.Errorf("default language = %q, want en", got)
	}

	// Persisted to disk.
	reloaded, err := settings.Load(storePath, settings.Data{})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Get(); got.NameRU != "Капитан" || got.RemoteTemperature != 0.3 || got.LocalTemperature != 0.2 {
		t.Errorf("persisted settings = %#v", got)
	}
}

func TestServerSettingsUpdateRejectsInvalidTemperature(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSettingsStore(mustLoadSettings(t))

	payload := `{"remote_temperature":3,"local_temperature":0.5}`
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func TestServerSettingsUpdateRejectsInvalidLanguage(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSettingsStore(mustLoadSettings(t))

	payload := `{"default_language":"fr"}`
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

type fakeTemperatureController struct {
	remote, local float64
}

func (f *fakeTemperatureController) SetTemperatures(remote, local float64) {
	f.remote, f.local = remote, local
}

func TestServerSettingsUpdateAppliesTemperatureController(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSettingsStore(mustLoadSettings(t))
	controller := &fakeTemperatureController{}
	server.SetTemperatureController(controller)

	payload := `{"remote_temperature":0.9,"local_temperature":0.1}`
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if controller.remote != 0.9 || controller.local != 0.1 {
		t.Errorf("controller = %#v, want {0.9 0.1}", controller)
	}
}

func TestServerCACertNotConfigured(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ca.pem", nil))
	if response.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.Code)
	}

	getResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResp, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var body settingsResponse
	if err := json.NewDecoder(getResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.CACertAvailable {
		t.Error("ca_cert_available = true, want false")
	}
}

func TestServerCACertDownload(t *testing.T) {
	const certContent = "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"
	certPath := filepath.Join(t.TempDir(), "rootCA.pem")
	if err := os.WriteFile(certPath, []byte(certContent), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetCACertFile(certPath)
	server.SetSettingsStore(mustLoadSettings(t))

	getResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResp, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var body settingsResponse
	if err := json.NewDecoder(getResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.CACertAvailable {
		t.Error("ca_cert_available = false, want true")
	}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ca.pem", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Body.String() != certContent {
		t.Errorf("body = %q, want %q", response.Body.String(), certContent)
	}
	if got := response.Header().Get("Content-Type"); got != "application/x-x509-ca-cert" {
		t.Errorf("Content-Type = %q", got)
	}
}

func mustLoadSettings(t *testing.T) *settings.Store {
	t.Helper()
	store, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"), settings.Data{})
	if err != nil {
		t.Fatalf("load settings store: %v", err)
	}
	return store
}
