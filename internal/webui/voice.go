package webui

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/voice"
)

// SetTTSEngine wires in text-to-speech (see docs/voice.md). Optional: nil
// (the default) means /api/tts reports the feature unavailable.
func (s *Server) SetTTSEngine(engine voice.TTSEngine) {
	s.ttsEngine = engine
}

type ttsRequest struct {
	Text string `json:"text"`
}

// handleTTS synthesizes speech for a chat message's text — used by the
// web UI's per-message "speak" button. Text is passed to the engine
// unmodified; punctuation and case are intonation cues, not noise to
// strip (see docs/voice.md).
func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	if s.ttsEngine == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "text-to-speech is not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request ttsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if request.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}

	start := time.Now()
	audio, err := s.ttsEngine.Synthesize(r.Context(), request.Text)
	elapsed := time.Since(start)
	if err != nil {
		s.logger.Error("tts synthesis failed", "error", err, "elapsed_ms", elapsed.Milliseconds())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "synthesis failed"})
		return
	}
	s.logger.Info("tts synthesis", "elapsed_ms", elapsed.Milliseconds(), "text_length", len(request.Text))

	w.Header().Set("Content-Type", "audio/wav")
	w.Write(audio)
}
