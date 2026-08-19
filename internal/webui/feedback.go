package webui

import (
	"encoding/json"
	"net/http"
)

type feedbackRequest struct {
	Text   string `json:"text"`
	Rating string `json:"rating"`
}

// handleFeedback records a 👍/👎 on a chat reply — logged only (no
// storage file), a satisfaction signal to grep for, not a feature with
// its own dashboard.
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if request.Rating != "up" && request.Rating != "down" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rating must be up or down"})
		return
	}
	s.logger.Info("chat feedback", "rating", request.Rating, "text", request.Text)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
