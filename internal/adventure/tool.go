package adventure

import (
	"context"
	"errors"
	"fmt"

	"github.com/roman220/bosun-smarthelper/internal/tools"
)

// Tool exposes the adventure game to the LLM via function calling. One
// tool, several actions, dispatched on the "action" argument — this
// keeps the model's decision ("what do I do next") simple: it always
// calls the same tool, just with different args, rather than choosing
// among several similarly-named tools.
//
// This is the *opportunistic* path — the LLM decides on its own,
// during normal conversation, when to invoke it, same as any other
// tool (weather, wikipedia, ...): its result gets narrated/incorporated
// by the model like everything else, no special-casing. The other,
// primary path is game mode (internal/webui's chat handler calling
// Store.Play directly, bypassing the LLM/tool loop entirely) — that's
// where config.AdventureConfig.NarrateLocal/NarrateRemote's raw-vs-
// narrated decision actually applies, since only a direct call, not a
// multi-step tool loop, can guarantee zero further LLM calls without
// silently dropping chained actions a user asked for in one message.
type Tool struct {
	store *Store
}

// NewTool builds the adventure tool from the store it persists sessions to.
func NewTool(store *Store) *Tool {
	return &Tool{store: store}
}

func (t *Tool) Name() string { return "adventure_game" }

func (t *Tool) Description() string {
	return "Play a text adventure game (a port of Colossal Cave Adventure). Actions: " +
		"\"list_sessions\" (no args) lists existing named game sessions; " +
		"\"new_session\" (session_name) creates and switches to a new one; " +
		"\"select_session\" (session_name) switches this conversation to an existing one; " +
		"\"command\" (command) sends one raw player command (e.g. \"go north\", \"look\", " +
		"\"take lamp\", \"inventory\") to the currently selected session and returns its " +
		"response text — do not invent or embellish the game's output yourself, and do not " +
		"skip ahead or resolve puzzles the engine hasn't actually resolved; " +
		"\"status\" (no args) reports the currently selected session without taking a turn. " +
		"A session must be created or selected before \"command\" works."
}

func (t *Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"list_sessions", "new_session", "select_session", "command", "status"},
			},
			"session_name": map[string]any{
				"type":        "string",
				"description": "Required for new_session and select_session.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Required for the command action: one raw player command.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (any, error) {
	chatSessionID, ok := tools.SessionIDFromContext(ctx)
	if !ok {
		chatSessionID = tools.DefaultCodeExecSessionID
	}

	action, _ := args["action"].(string)
	switch action {
	case "list_sessions":
		return t.listSessions()
	case "new_session":
		name, _ := args["session_name"].(string)
		return t.newSession(chatSessionID, name)
	case "select_session":
		name, _ := args["session_name"].(string)
		return t.selectSession(chatSessionID, name)
	case "command":
		command, _ := args["command"].(string)
		return t.command(chatSessionID, command)
	case "status":
		return t.status(chatSessionID)
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

func (t *Tool) listSessions() (any, error) {
	infos, err := t.store.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sessions := make([]map[string]any, len(infos))
	for i, info := range infos {
		sessions[i] = map[string]any{
			"name":        info.Name,
			"turns":       info.Turns,
			"location_id": info.LocationID,
			"game_over":   info.GameOver,
			"updated_at":  info.UpdatedAt,
		}
	}
	return map[string]any{"sessions": sessions}, nil
}

func (t *Tool) newSession(chatSessionID, name string) (any, error) {
	if name == "" {
		return nil, errors.New("session_name is required")
	}

	game, err := t.store.CreateSession(name, 0)
	if err != nil {
		return nil, fmt.Errorf("create session %q: %w", name, err)
	}
	if err := t.store.SetActiveSession(chatSessionID, name); err != nil {
		return nil, err
	}

	return turnResult(game.Output, game.Loc, true, game.GameOver), nil
}

func (t *Tool) selectSession(chatSessionID, name string) (any, error) {
	if name == "" {
		return nil, errors.New("session_name is required")
	}

	game, err := t.store.LoadSession(name)
	if err != nil {
		return nil, fmt.Errorf("select session %q: %w", name, err)
	}
	if err := t.store.SetActiveSession(chatSessionID, name); err != nil {
		return nil, err
	}

	return turnResult(game.Output, game.Loc, true, game.GameOver), nil
}

func (t *Tool) command(chatSessionID, command string) (any, error) {
	if command == "" {
		return nil, errors.New("command is required")
	}

	name, ok, err := t.store.ActiveSession(chatSessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("no active game session — use new_session or select_session first")
	}

	// Language "" (English) here, not a UI language setting — this tool
	// call path is always narrated by the main model afterward (unlike
	// game mode's direct, zero-LLM path), so the model already speaks
	// whatever language the conversation is in regardless of the raw
	// engine text's own language.
	output, locationID, locationChanged, gameOver, err := t.store.Play(name, command, "")
	if err != nil {
		return nil, fmt.Errorf("play %q: %w", name, err)
	}

	return turnResult(output, locationID, locationChanged, gameOver), nil
}

func (t *Tool) status(chatSessionID string) (any, error) {
	name, ok, err := t.store.ActiveSession(chatSessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]any{"active_session": nil}, nil
	}

	infos, err := t.store.ListSessions()
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Name == name {
			return map[string]any{
				"active_session": info.Name,
				"turns":          info.Turns,
				"location_id":    info.LocationID,
				"game_over":      info.GameOver,
			}, nil
		}
	}
	return map[string]any{"active_session": name}, nil
}

// turnResult builds the tool's return value for anything that produces
// game output text — the raw engine text plus enough state for the
// model to reference naturally (location, whether it just changed,
// whether the game ended). The model is free to narrate or quote this
// as it sees fit, same as any other tool's result.
func turnResult(text string, locationID int32, locationChanged, gameOver bool) map[string]any {
	return map[string]any{
		"text":             text,
		"location_id":      locationID,
		"location_changed": locationChanged,
		"game_over":        gameOver,
	}
}

var _ tools.Tool = (*Tool)(nil)
