package webui

import (
	"encoding/json"
	"net/http"
)

// providerOverrideController matches *llm.Router's manual online/offline
// override — the auto-selected provider (based on connectivity and
// prefer_remote) stays the default; this is a session-only lever next to
// the web UI's status pill, not a persisted setting.
type providerOverrideController interface {
	SetProviderOverride(mode string) error
	ProviderOverride() string
}

// SetProviderOverrideController wires in the online/offline manual
// switch. Optional: nil (the default) means /api/provider-override
// reports the feature unavailable and GET /api/status omits
// provider_override entirely.
func (s *Server) SetProviderOverrideController(controller providerOverrideController) {
	s.providerOverride = controller
}

type providerOverrideRequest struct {
	Override string `json:"override"`
}

// handleProviderOverride sets the manual online/offline switch: "auto"
// (or "") restores automatic connectivity-based selection, "local" or
// "remote" forces that provider until changed again or the process
// restarts.
func (s *Server) handleProviderOverride(w http.ResponseWriter, r *http.Request) {
	if s.providerOverride == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider override is not available"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request providerOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.providerOverride.SetProviderOverride(request.Override); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"override": s.providerOverride.ProviderOverride()})
}
