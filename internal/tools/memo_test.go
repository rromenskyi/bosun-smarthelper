package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestMemoToolWriteReadArchiveDelete(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")})
	ctx := context.Background()

	written, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "shopping", "content": "Buy milk",
	})
	if err != nil {
		t.Fatalf("write memo: %v", err)
	}
	writtenMemo := written.(map[string]any)
	if writtenMemo["created_at"] == "" || writtenMemo["updated_at"] == "" {
		t.Errorf("memo timestamps are missing: %#v", writtenMemo)
	}

	read, err := tool.Execute(ctx, map[string]any{"action": "read", "key": "shopping"})
	if err != nil {
		t.Fatalf("read memo: %v", err)
	}
	if read.(map[string]any)["content"] != "Buy milk" {
		t.Errorf("unexpected memo: %#v", read)
	}

	if _, err := tool.Execute(ctx, map[string]any{"action": "archive", "key": "shopping"}); err != nil {
		t.Fatalf("archive memo: %v", err)
	}
	active, err := tool.Execute(ctx, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list active memos: %v", err)
	}
	if active.(map[string]any)["count"] != 0 {
		t.Errorf("active memo count = %v, want 0", active.(map[string]any)["count"])
	}
	all, err := tool.Execute(ctx, map[string]any{"action": "list", "include_archived": true})
	if err != nil {
		t.Fatalf("list all memos: %v", err)
	}
	if all.(map[string]any)["count"] != 1 {
		t.Errorf("all memo count = %v, want 1", all.(map[string]any)["count"])
	}

	if _, err := tool.Execute(ctx, map[string]any{"action": "delete", "key": "shopping"}); err != nil {
		t.Fatalf("delete memo: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "read", "key": "shopping"}); err == nil {
		t.Fatal("expected deleted memo to be missing")
	}
}

func TestMemoToolPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memos.json")
	first := NewMemoTool(&config.MemoConfig{Path: path})
	if _, err := first.Execute(context.Background(), map[string]any{
		"action": "write", "key": "persistent", "content": "Remember me",
	}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	second := NewMemoTool(&config.MemoConfig{Path: path})
	result, err := second.Execute(context.Background(), map[string]any{"action": "read", "key": "persistent"})
	if err != nil {
		t.Fatalf("read persisted memo: %v", err)
	}
	if result.(map[string]any)["content"] != "Remember me" {
		t.Errorf("unexpected persisted memo: %#v", result)
	}
}
