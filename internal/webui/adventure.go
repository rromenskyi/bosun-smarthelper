package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

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

type adventureSessionSummary struct {
	Name       string `json:"name"`
	Turns      int    `json:"turns"`
	LocationID int32  `json:"location_id"`
	GameOver   bool   `json:"game_over"`
	UpdatedAt  string `json:"updated_at"`
}

// handleAdventureSessionsList backs the settings page's session picker —
// plain, LLM-free, same as everything else in this file.
func (s *Server) handleAdventureSessionsList(w http.ResponseWriter, r *http.Request) {
	if s.adventureStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "adventure feature is not enabled"})
		return
	}
	infos, err := s.adventureStore.ListSessions()
	if err != nil {
		s.logger.Error("list adventure sessions", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list sessions"})
		return
	}
	sessions := make([]adventureSessionSummary, len(infos))
	for i, info := range infos {
		sessions[i] = adventureSessionSummary{
			Name:       info.Name,
			Turns:      info.Turns,
			LocationID: info.LocationID,
			GameOver:   info.GameOver,
			UpdatedAt:  info.UpdatedAt.Format(time.RFC3339),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

type adventureSessionCreateRequest struct {
	Name string `json:"name"`
	Seed int    `json:"seed"`
}

// handleAdventureSessionCreate makes a new named session. Does not itself
// put any conversation into game mode or select an active session — the
// settings page's "new" button and a subsequent explicit select/mode
// call are two separate, deliberate steps.
func (s *Server) handleAdventureSessionCreate(w http.ResponseWriter, r *http.Request) {
	if s.adventureStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "adventure feature is not enabled"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request adventureSessionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if _, err := s.adventureStore.CreateSession(name, request.Seed); err != nil {
		if err == adventure.ErrSessionExists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a session with that name already exists"})
			return
		}
		s.logger.Error("create adventure session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create session"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name})
}

type adventureSessionRenameRequest struct {
	NewName string `json:"new_name"`
}

// handleAdventureSessionRename renames a session in place.
func (s *Server) handleAdventureSessionRename(w http.ResponseWriter, r *http.Request) {
	if s.adventureStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "adventure feature is not enabled"})
		return
	}
	oldName := strings.TrimSpace(r.PathValue("name"))
	if oldName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request adventureSessionRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	newName := strings.TrimSpace(request.NewName)
	if newName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new_name is required"})
		return
	}
	if err := s.adventureStore.RenameSession(oldName, newName); err != nil {
		switch err {
		case adventure.ErrSessionNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		case adventure.ErrSessionExists:
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a session with that name already exists"})
		default:
			s.logger.Error("rename adventure session", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not rename session"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": newName})
}

// handleAdventureSessionDelete removes a session. Any chat conversation
// still pointed at it via game mode will simply get "session not found"
// on its next turn — no attempt to hunt down and clear those pointers
// here, since they're keyed by chat session id, not by adventure session
// name, and are naturally cleaned up as chat sessions expire (see
// SessionOptions.TTL).
func (s *Server) handleAdventureSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.adventureStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "adventure feature is not enabled"})
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := s.adventureStore.DeleteSession(name); err != nil {
		if err == adventure.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		s.logger.Error("delete adventure session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete session"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}
