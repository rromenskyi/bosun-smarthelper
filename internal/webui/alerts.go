package webui

import (
	"context"
	"encoding/json"
	"net/http"
)

// SetAlertsTestSender wires in a function that actually delivers one test
// alert through a specific channel — built in cmd/smarthelper/main.go,
// which owns the real config.AlertsChannelsConfig and TTS engine, so
// internal/webui never needs to import internal/alerts itself. Optional:
// nil (the default) means the test endpoint always reports the channel as
// unavailable, same "wiring absent means feature reports itself off"
// pattern as SetBackupConfig/SetCameraManager.
func (s *Server) SetAlertsTestSender(fn func(ctx context.Context, channel string) error) {
	s.alertsTestSender = fn
}

type alertsTestRequest struct {
	Channel string `json:"channel"`
}

// handleAlertsTest fires one real, harmless test notification through a
// single channel — the only way to find out *before* a real NOAA alert or
// threshold crossing whether that channel actually delivers (a wrong bot
// token, an unreachable webhook URL, a speaker channel with no working
// audio device all fail silently otherwise; see docs/alerts.md's
// at-most-once section). Deliberately ignores each channel's own
// AlertsXEnabled toggle (internal/settings) — testing is exactly how a
// human decides whether to flip that toggle on in the first place, so
// requiring it to already be on would defeat the point.
func (s *Server) handleAlertsTest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request alertsTestRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	configured := map[string]bool{
		"telegram": s.alertsConfigured.Telegram,
		"webhook":  s.alertsConfigured.Webhook,
		"speaker":  s.alertsConfigured.Speaker,
	}
	ok, known := configured[request.Channel]
	if !known || !ok || s.alertsTestSender == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "channel is not configured"})
		return
	}
	if err := s.alertsTestSender(r.Context(), request.Channel); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}
