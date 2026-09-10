package tools

import (
	"context"
	"testing"
)

type fakeHistoryProvider struct {
	histories map[string][]HistoryEntry
}

func (p *fakeHistoryProvider) SessionHistory(sessionID string) ([]HistoryEntry, bool) {
	h, ok := p.histories[sessionID]
	return h, ok
}

func TestSearchHistoryToolFindsMatchingMessages(t *testing.T) {
	provider := &fakeHistoryProvider{histories: map[string][]HistoryEntry{
		"session-1": {
			{Role: "user", Content: "we're planning a hunting trip in Utah"},
			{Role: "assistant", Content: "sounds fun, what species?"},
			{Role: "user", Content: "elk, buck, ducks and geese, whitetail deer"},
		},
	}}
	tool := NewSearchHistoryTool(provider)
	ctx := ContextWithSessionID(context.Background(), "session-1")

	result, err := tool.Execute(ctx, map[string]any{"query": "whitetail"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != 1 {
		t.Errorf("count = %#v, want 1", view["count"])
	}
	matches := view["matches"].([]map[string]any)
	if len(matches) != 1 || matches[0]["role"] != "user" {
		t.Errorf("matches = %#v", matches)
	}
}

func TestSearchHistoryToolIsCaseInsensitive(t *testing.T) {
	provider := &fakeHistoryProvider{histories: map[string][]HistoryEntry{
		"session-1": {{Role: "user", Content: "License number ABC123"}},
	}}
	tool := NewSearchHistoryTool(provider)
	ctx := ContextWithSessionID(context.Background(), "session-1")

	result, err := tool.Execute(ctx, map[string]any{"query": "abc123"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.(map[string]any)["count"] != 1 {
		t.Errorf("expected a case-insensitive match, got %#v", result)
	}
}

func TestSearchHistoryToolNoMatches(t *testing.T) {
	provider := &fakeHistoryProvider{histories: map[string][]HistoryEntry{
		"session-1": {{Role: "user", Content: "hello"}},
	}}
	tool := NewSearchHistoryTool(provider)
	ctx := ContextWithSessionID(context.Background(), "session-1")

	result, err := tool.Execute(ctx, map[string]any{"query": "nonexistent"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.(map[string]any)["count"] != 0 {
		t.Errorf("expected no matches, got %#v", result)
	}
}

func TestSearchHistoryToolRequiresQuery(t *testing.T) {
	tool := NewSearchHistoryTool(&fakeHistoryProvider{})
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := tool.Execute(ctx, map[string]any{}); err == nil {
		t.Error("expected an error when query is missing")
	}
}

func TestSearchHistoryToolRequiresSessionID(t *testing.T) {
	tool := NewSearchHistoryTool(&fakeHistoryProvider{})
	if _, err := tool.Execute(context.Background(), map[string]any{"query": "x"}); err == nil {
		t.Error("expected an error when no session id is in context")
	}
}

func TestSearchHistoryToolRequiresProvider(t *testing.T) {
	tool := NewSearchHistoryTool(nil)
	ctx := ContextWithSessionID(context.Background(), "session-1")
	if _, err := tool.Execute(ctx, map[string]any{"query": "x"}); err == nil {
		t.Error("expected an error when no provider is configured")
	}
}

func TestSearchHistoryToolCapsResultCount(t *testing.T) {
	entries := make([]HistoryEntry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, HistoryEntry{Role: "user", Content: "match me"})
	}
	provider := &fakeHistoryProvider{histories: map[string][]HistoryEntry{"session-1": entries}}
	tool := NewSearchHistoryTool(provider)
	ctx := ContextWithSessionID(context.Background(), "session-1")

	result, err := tool.Execute(ctx, map[string]any{"query": "match"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := result.(map[string]any)["count"]; got != maxSearchHistoryResults {
		t.Errorf("count = %#v, want capped at %d", got, maxSearchHistoryResults)
	}
}

func TestSearchHistoryToolUnknownSessionReturnsEmpty(t *testing.T) {
	tool := NewSearchHistoryTool(&fakeHistoryProvider{histories: map[string][]HistoryEntry{}})
	ctx := ContextWithSessionID(context.Background(), "never-existed")

	result, err := tool.Execute(ctx, map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.(map[string]any)["count"] != 0 {
		t.Errorf("expected no matches for an unknown session, got %#v", result)
	}
}
