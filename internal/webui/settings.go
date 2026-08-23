package webui

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/roman220/bosun-smarthelper/internal/settings"
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

// SetCACertFile makes the CA that issued the TLS cert (e.g. mkcert's
// rootCA.pem) downloadable at GET /ca.pem and linked from the settings
// page, so a new device can grab and trust it directly instead of a
// separate file transfer — see docs/tls.md. Empty (the default) hides the
// link and 404s the route. Never point this at a CA's private key.
func (s *Server) SetCACertFile(path string) {
	s.caCertFile = path
}

// SetAlertsConfigured records which alert channels (docs/alerts.md) are
// actually set up in config.yaml/.env, so the settings page only shows a
// toggle for a channel that would do something if enabled. Each flag is
// independent — e.g. Telegram configured, webhook and speaker not.
func (s *Server) SetAlertsConfigured(telegram, webhook, speaker bool) {
	s.alertsConfigured = alertsConfigured{Telegram: telegram, Webhook: webhook, Speaker: speaker}
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	data := s.settingsStore.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                    true,
		"settings":                   data,
		"ca_cert_available":          s.caCertFile != "",
		"backup_configured":          s.backupS3Cfg != nil,
		"alerts_telegram_configured": s.alertsConfigured.Telegram,
		"alerts_webhook_configured":  s.alertsConfigured.Webhook,
		"alerts_speaker_configured":  s.alertsConfigured.Speaker,
	})
}

// handleCACert serves the CA certificate set via SetCACertFile so a new
// device can download and trust it directly from the running service.
// application/x-x509-ca-cert is the MIME type iOS Safari recognizes to
// offer installing it as a trusted profile.
func (s *Server) handleCACert(w http.ResponseWriter, r *http.Request) {
	if s.caCertFile == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(s.caCertFile)
	if err != nil {
		s.logger.Error("read CA cert", "error", err)
		http.Error(w, "CA certificate unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="bosun-ca.pem"`)
	w.Write(data)
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
	if data.BackupAutoEnabled && data.BackupIntervalHours <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backup_interval_hours must be positive when backup_auto_enabled is true"})
		return
	}
	for _, rule := range data.AlertsThresholds {
		if rule.Metric == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "every alert threshold needs a metric"})
			return
		}
		if !validThresholdOperator(rule.Operator) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "alert threshold operator must be one of >, <, >=, <=, =="})
			return
		}
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

func validThresholdOperator(op string) bool {
	switch op {
	case ">", "<", ">=", "<=", "==":
		return true
	default:
		return false
	}
}
