package tools

import (
	"context"
	"fmt"
	"strings"
)

// HistoryEntry is one message from a chat session's stored conversation
// history — a local, minimal shape rather than agent.HistoryMessage
// directly, since this package can't import internal/agent (agent
// already imports tools, for the Registry).
type HistoryEntry struct {
	Role    string
	Content string
}

// HistoryProvider is implemented by *webui.Server, the one place that
// actually owns session storage.
type HistoryProvider interface {
	SessionHistory(sessionID string) ([]HistoryEntry, bool)
}

// maxSearchHistoryResults bounds a single search_history call's result —
// this is meant to help the model find one specific thing it half-
// remembers, not to page through the whole conversation.
const maxSearchHistoryResults = 10

// SearchHistoryTool lets the model search the FULL, original chat
// history — the escape hatch for whatever's currently folded into a
// prose summary to keep the outgoing prompt small (see
// agent.CompactHistory, docs/settings.md): the summary is what the model
// sees by default, but the real messages it was built from are never
// deleted, and this is how the model reaches them on demand instead of
// just guessing when the user says "didn't I already tell you...".
type SearchHistoryTool struct {
	provider HistoryProvider
}

// NewSearchHistoryTool wires the tool to its session-history source.
// provider may be nil (matching how other optional features degrade
// elsewhere in this codebase) — Execute then returns a clear error
// instead of the tool failing to register at all.
func NewSearchHistoryTool(provider HistoryProvider) *SearchHistoryTool {
	return &SearchHistoryTool{provider: provider}
}

func (t *SearchHistoryTool) Name() string { return "search_history" }

func (t *SearchHistoryTool) Description() string {
	return "Search this chat's full conversation history for a keyword or phrase — including the older middle of a long conversation, " +
		"which gets folded into a short summary in your visible context to save space, but is never actually deleted. " +
		"Use this when the user references something specific from earlier you don't have exact details on " +
		"(e.g. \"didn't I already tell you...\", \"what was that number again\") instead of guessing or claiming you don't know."
}

func (t *SearchHistoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "A keyword or short phrase to search for (case-insensitive).",
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func (t *SearchHistoryTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if t.provider == nil {
		return nil, fmt.Errorf("chat history search is not configured")
	}
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no chat session available for history search")
	}
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	history, ok := t.provider.SessionHistory(sessionID)
	if !ok {
		return map[string]any{"matches": []map[string]any{}, "count": 0}, nil
	}

	needle := strings.ToLower(query)
	matches := make([]map[string]any, 0, maxSearchHistoryResults)
	for _, entry := range history {
		if !strings.Contains(strings.ToLower(entry.Content), needle) {
			continue
		}
		matches = append(matches, map[string]any{"role": entry.Role, "content": entry.Content})
		if len(matches) >= maxSearchHistoryResults {
			break
		}
	}
	return map[string]any{"matches": matches, "count": len(matches)}, nil
}
