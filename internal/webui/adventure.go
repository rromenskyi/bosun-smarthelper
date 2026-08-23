package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/roman220/bosun-smarthelper/internal/adventure"
	"github.com/roman220/bosun-smarthelper/internal/llm"
)

// adventureNarrator matches *llm.Router — used only for game mode's
// optional narration step: a single plain (tool-less) call, never the
// full agent/tool-calling loop the opportunistic adventure_game LLM
// tool goes through (see internal/adventure.Tool and docs/adventure.md
// for why those are two separate, deliberately different paths).
type adventureNarrator interface {
	Chat(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition) (*llm.Response, error)
	ActiveProvider() string
}

// SetAdventureStore wires in the persistence layer game mode reads and
// writes directly. Optional: nil (the default, or when adventure.enabled
// is false) makes /api/adventure/mode always report the feature
// unavailable.
func (s *Server) SetAdventureStore(store *adventure.Store) {
	s.adventureStore = store
}

// SetAdventureNarrator wires the object (typically *llm.Router) game mode
// uses to optionally rephrase raw engine output, plus the two settings
// (config.AdventureConfig.NarrateLocal/NarrateRemote) deciding whether
// that actually happens for the currently active provider. Leaving this
// unset means game mode always replies with the engine's raw text.
func (s *Server) SetAdventureNarrator(narrator adventureNarrator, narrateLocal, narrateRemote bool) {
	s.adventureNarrator = narrator
	s.adventureNarrateLocal = narrateLocal
	s.adventureNarrateRemote = narrateRemote
}

type adventureModeRequest struct {
	SessionID        string `json:"session_id"`
	Enabled          bool   `json:"enabled"`
	AdventureSession string `json:"adventure_session"`
}

// handleAdventureMode flips a chat conversation into or out of game
// mode. Turning it on requires naming an existing session — this is a
// plain, LLM-free action, deliberately, so choosing which game to play
// never depends on a model being available to ask.
func (s *Server) handleAdventureMode(w http.ResponseWriter, r *http.Request) {
	if s.adventureStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "adventure feature is not enabled"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request adventureModeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	sessionID := strings.TrimSpace(request.SessionID)
	if !validSessionID(sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session_id"})
		return
	}

	if !request.Enabled {
		s.setAdventureMode(sessionID, false, "")
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	name := strings.TrimSpace(request.AdventureSession)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "adventure_session is required"})
		return
	}
	if _, err := s.adventureStore.LoadSession(name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "adventure session not found"})
		return
	}
	if err := s.adventureStore.SetActiveSession(sessionID, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not set active session"})
		return
	}
	s.setAdventureMode(sessionID, true, name)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "adventure_session": name})
}

// handleAdventureTurn is game mode's per-message path, called from
// handleChat once it's confirmed the conversation is in game mode: apply
// the command directly to the game engine, no LLM/tool-calling loop
// involved unless narration is on for the currently active provider —
// and even then, exactly one plain call, never a multi-step loop, so a
// turn can never silently chain into more actions than the one the user
// actually asked for (see docs/adventure.md for why that matters).
func (s *Server) handleAdventureTurn(w http.ResponseWriter, ctx context.Context, sessionID, message, adventureSessionName string) {
	output, locationID, changed, gameOver, err := s.adventureStore.Play(adventureSessionName, message)
	if err != nil {
		s.logger.Warn("adventure turn failed", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusBadGateway, chatResponse{Error: "adventure turn failed"})
		return
	}

	reply := output
	if s.adventureShouldNarrate() {
		if narrated, err := s.narrateAdventureOutput(ctx, output); err != nil {
			s.logger.Warn("adventure narration failed; using raw text", "error", err)
		} else if narrated != "" {
			reply = narrated
		}
	}

	s.saveUserMessage(sessionID, message)
	s.saveAssistantReply(sessionID, reply)

	response := chatResponse{Answer: reply, SessionID: sessionID}
	if changed {
		response.LocationID = &locationID
	}
	writeJSON(w, http.StatusOK, response)

	if gameOver {
		s.setAdventureMode(sessionID, false, "")
	}
}

func (s *Server) adventureShouldNarrate() bool {
	if s.adventureNarrator == nil {
		return false
	}
	if s.adventureNarrator.ActiveProvider() == "local" {
		return s.adventureNarrateLocal
	}
	return s.adventureNarrateRemote
}

func (s *Server) narrateAdventureOutput(ctx context.Context, text string) (string, error) {
	messages := []llm.Message{
		{Role: "system", Content: "Rephrase the following text-adventure game output naturally, in character, " +
			"without adding any new information, objects, or events beyond what it already says. Keep it concise."},
		{Role: "user", Content: text},
	}
	resp, err := s.adventureNarrator.Chat(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
