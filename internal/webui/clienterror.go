package webui

import (
	"encoding/json"
	"net/http"
)

type clientErrorRequest struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Context string `json:"context"`
}

// handleClientError records a chat request that failed entirely on the
// client side — a network error before the request left the browser, a
// connection dropped mid-stream, or a malformed response. These never
// reach handleChat/handleChatStreaming's own error logging
// (s.logger.Error("web chat failed", ...)) because the server-side
// request/response cycle never happened or never completed, so without
// this there is otherwise zero trace anywhere of what went wrong —
// index.html's ask() previously just swallowed the real error into a
// generic on-screen message. Logged only, no storage — same pattern as
// handleFeedback.
func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request clientErrorRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	s.logger.Warn("client-side chat request failed",
		"name", request.Name, "message", request.Message, "context", request.Context)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
