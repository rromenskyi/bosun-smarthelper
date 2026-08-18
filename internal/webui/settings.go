package webui

import (
	"encoding/json"
	"net/http"

	"github.com/roman220/ai-local-smarthelper/internal/settings"
)

// personaSetter matches *agent.Agent's SetPersona — checked via type
// assertion on s.asker, the same duck-typing pattern as conversationAsker.
type personaSetter interface {
	SetPersona(nameRU, nameEN, stylePrompt string)
}

// temperatureController matches *llm.Router's SetTemperatures.
type temperatureController interface {
	SetTemperatures(remote, local float64)
}

// SetSettingsStore wires in the live-editable settings page (see
// docs/settings.md) — persona/style prompt, default language, LLM
// temperatures, memo tag canonicalization vocabulary. Optional: nil (the
// default) means /api/settings reports the feature as disabled rather
// than erroring.
func (s *Server) SetSettingsStore(store *settings.Store) {
	s.settingsStore = store
}

// SetTemperatureController wires in the object whose SetTemperatures
// applies a settings-page temperature change live — typically *llm.Router.
// Optional: nil means a temperature change is saved but has no live
// effect until the next restart.
func (s *Server) SetTemperatureController(controller temperatureController) {
	s.temps = controller
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	data := s.settingsStore.Get()
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "settings": data})
}

// handleSettingsUpdate persists the new settings, then applies each one
// live to whatever's already running — no restart needed. A field's live
// application is best-effort (e.g. temperatures do nothing if no
// TemperatureController was wired up); the save itself either fully
// succeeds or fully fails.
func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "settings are not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var data settings.Data
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if data.DefaultLanguage != "" && data.DefaultLanguage != "ru" && data.DefaultLanguage != "en" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "default_language must be ru or en"})
		return
	}
	if data.RemoteTemperature < 0 || data.RemoteTemperature > 2 || data.LocalTemperature < 0 || data.LocalTemperature > 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "temperatures must be between 0 and 2"})
		return
	}

	if err := s.settingsStore.Update(data); err != nil {
		s.logger.Error("save settings", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save settings"})
		return
	}
	data = s.settingsStore.Get() // normalized (trimmed etc.) by Update

	if setter, ok := s.asker.(personaSetter); ok {
		setter.SetPersona(data.NameRU, data.NameEN, data.StylePrompt)
	}
	if s.temps != nil {
		s.temps.SetTemperatures(data.RemoteTemperature, data.LocalTemperature)
	}
	if data.DefaultLanguage != "" {
		s.SetDefaultLanguage(data.DefaultLanguage)
	}

	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "settings": data})
}
