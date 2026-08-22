package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/llm"
)

type fakeChatClient struct {
	response string
	// responseFn, when set, takes priority over response — used where a
	// test needs to answer based on the actual prompt content rather than
	// a fixed string, e.g. because candidate numbering depends on
	// NormalizeTags' iteration order over a map (Go's map iteration order
	// is randomized, so a batch of more than one candidate can't assume
	// "1" is always the same memo across runs).
	responseFn func(prompt string) string
	seen       []llm.Message
	err        error
}

func (f *fakeChatClient) Chat(_ context.Context, messages []llm.Message, _ []llm.ToolDefinition) (*llm.Response, error) {
	f.seen = messages
	if f.err != nil {
		return nil, f.err
	}
	if f.responseFn != nil {
		prompt := ""
		if len(messages) > 0 {
			prompt = messages[len(messages)-1].Content
		}
		return &llm.Response{Content: f.responseFn(prompt)}, nil
	}
	return &llm.Response{Content: f.response}, nil
}

func TestNormalizeTagsMapsToCanonicalSet(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "fuel-pump", "content": "Replaced the fuel pump",
		"tags": []any{"бензонасос", "ремонт"},
	}); err != nil {
		t.Fatalf("write memo: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "groceries", "content": "Bought supplies",
		"tags": []any{"покупки"},
	}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	// NormalizeTags batches candidates by iterating a map, so which memo
	// lands on line "1" vs "2" isn't guaranteed — answer by matching each
	// line's actual tags instead of assuming a fixed position.
	client := &fakeChatClient{responseFn: func(prompt string) string {
		var lines []string
		for _, line := range strings.Split(prompt, "\n") {
			number, tagsPart, ok := strings.Cut(line, ". tags: ")
			if !ok {
				continue
			}
			switch {
			case strings.Contains(tagsPart, "покупки"):
				lines = append(lines, number+": purchases")
			case strings.Contains(tagsPart, "бензонасос"):
				lines = append(lines, number+": fuel_system, maintenance")
			}
		}
		return strings.Join(lines, "\n")
	}}
	updated, err := tool.NormalizeTags(ctx, client, []string{"fuel_system", "maintenance", "purchases", "oil"}, 10)
	if err != nil {
		t.Fatalf("NormalizeTags: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}

	read, err := tool.Execute(ctx, map[string]any{"action": "read", "key": "fuel-pump"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	view := read.(map[string]any)
	canonical, _ := view["canonical_tags"].([]string)
	if len(canonical) != 2 || canonical[0] != "fuel_system" || canonical[1] != "maintenance" {
		t.Errorf("canonical_tags = %#v, want [fuel_system maintenance]", view["canonical_tags"])
	}
	tags, _ := view["tags"].([]string)
	if len(tags) != 2 || tags[0] != "бензонасос" {
		t.Errorf("original tags were destroyed: %#v", view["tags"])
	}

	// A memo whose tags were already normalized is skipped on the next pass.
	client2 := &fakeChatClient{response: "1: oil\n"}
	updatedAgain, err := tool.NormalizeTags(ctx, client2, []string{"fuel_system", "maintenance", "purchases", "oil"}, 10)
	if err != nil {
		t.Fatalf("second NormalizeTags: %v", err)
	}
	if updatedAgain != 0 {
		t.Errorf("updatedAgain = %d, want 0 (nothing left to normalize)", updatedAgain)
	}
}

func TestNormalizeTagsSkipsNoneAndUnparseableLines(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "random", "content": "Saw a dolphin",
		"tags": []any{"wildlife"},
	}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	client := &fakeChatClient{response: "1: none\n"}
	updated, err := tool.NormalizeTags(ctx, client, []string{"purchases", "maintenance"}, 10)
	if err != nil {
		t.Fatalf("NormalizeTags: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0 for a 'none' response", updated)
	}

	read, _ := tool.Execute(ctx, map[string]any{"action": "read", "key": "random"})
	if _, ok := read.(map[string]any)["canonical_tags"]; ok {
		t.Error("canonical_tags should not be set when the model said none")
	}
}

func TestNormalizeTagsNoOpWithoutCanonicalTagsOrCandidates(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	client := &fakeChatClient{response: "should not be read"}
	if updated, err := tool.NormalizeTags(ctx, client, nil, 10); err != nil || updated != 0 {
		t.Errorf("empty canonicalTags: updated=%d err=%v, want 0, nil", updated, err)
	}
	if client.seen != nil {
		t.Error("Chat should never be called when canonicalTags is empty")
	}

	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "untagged", "content": "no tags here"}); err != nil {
		t.Fatalf("write memo: %v", err)
	}
	if updated, err := tool.NormalizeTags(ctx, client, []string{"purchases"}, 10); err != nil || updated != 0 {
		t.Errorf("no tagged memos: updated=%d err=%v, want 0, nil", updated, err)
	}
	if client.seen != nil {
		t.Error("Chat should never be called when there are no candidates")
	}
}
