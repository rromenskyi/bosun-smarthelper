package adventure

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/tools"
)

func newTestTool(t *testing.T) *Tool {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewTool(store)
}

func TestToolNewSessionThenCommand(t *testing.T) {
	tool := newTestTool(t)
	ctx := tools.ContextWithSessionID(context.Background(), "chat-abc")

	result, err := tool.Execute(ctx, map[string]any{"action": "new_session", "session_name": "quest1"})
	if err != nil {
		t.Fatalf("new_session failed: %v", err)
	}
	res := result.(map[string]any)
	if res["text"].(string) == "" {
		t.Error("expected non-empty text from new_session")
	}

	result, err = tool.Execute(ctx, map[string]any{"action": "command", "command": "look"})
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
	res = result.(map[string]any)
	if res["text"].(string) == "" {
		t.Error("expected non-empty text from command")
	}
}

func TestToolCommandWithoutActiveSessionFails(t *testing.T) {
	tool := newTestTool(t)
	ctx := tools.ContextWithSessionID(context.Background(), "chat-abc")

	if _, err := tool.Execute(ctx, map[string]any{"action": "command", "command": "look"}); err == nil {
		t.Error("expected an error when no session is active")
	}
}

func TestToolListAndSelectSession(t *testing.T) {
	tool := newTestTool(t)
	ctxA := tools.ContextWithSessionID(context.Background(), "chat-a")
	ctxB := tools.ContextWithSessionID(context.Background(), "chat-b")

	if _, err := tool.Execute(ctxA, map[string]any{"action": "new_session", "session_name": "shared"}); err != nil {
		t.Fatalf("new_session failed: %v", err)
	}

	result, err := tool.Execute(ctxA, map[string]any{"action": "list_sessions"})
	if err != nil {
		t.Fatalf("list_sessions failed: %v", err)
	}
	sessions := result.(map[string]any)["sessions"].([]map[string]any)
	if len(sessions) != 1 || sessions[0]["name"] != "shared" {
		t.Fatalf("unexpected sessions list: %+v", sessions)
	}

	// A second chat conversation can select the same named session.
	if _, err := tool.Execute(ctxB, map[string]any{"action": "select_session", "session_name": "shared"}); err != nil {
		t.Fatalf("select_session failed: %v", err)
	}
	if _, err := tool.Execute(ctxB, map[string]any{"action": "command", "command": "look"}); err != nil {
		t.Fatalf("command from second conversation failed: %v", err)
	}
}

func TestToolStatusWithNoActiveSession(t *testing.T) {
	tool := newTestTool(t)
	ctx := tools.ContextWithSessionID(context.Background(), "chat-abc")

	result, err := tool.Execute(ctx, map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if result.(map[string]any)["active_session"] != nil {
		t.Errorf("expected nil active_session, got %+v", result)
	}
}
