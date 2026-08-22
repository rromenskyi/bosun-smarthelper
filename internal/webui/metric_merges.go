package webui

import (
	"encoding/json"
	"net/http"

	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

// SetMemoTool wires in the memo tool so the web UI can surface its
// metric-merge approval queue (docs/maintenance-tracking.md) directly,
// outside the chat/tool-call path — approving or rejecting a suggestion is
// a human-only decision, never something the model does itself. Optional:
// nil (the default) means the endpoints below just report an empty queue.
func (s *Server) SetMemoTool(tool *tools.MemoTool) {
	s.memoTool = tool
}

func (s *Server) handleMetricMergesList(w http.ResponseWriter, r *http.Request) {
	if s.memoTool == nil {
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": []tools.MetricMergeSuggestion{}})
		return
	}
	suggestions, err := s.memoTool.MetricMergeSuggestions()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list metric merge suggestions"})
		return
	}
	if suggestions == nil {
		suggestions = []tools.MetricMergeSuggestion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": suggestions})
}

type metricMergeDecideRequest struct {
	Approve bool `json:"approve"`
}

func (s *Server) handleMetricMergeDecide(w http.ResponseWriter, r *http.Request) {
	if s.memoTool == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "metric merge suggestions are not available"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request metricMergeDecideRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	suggestion, err := s.memoTool.DecideMetricMerge(r.PathValue("id"), request.Approve)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, suggestion)
}
