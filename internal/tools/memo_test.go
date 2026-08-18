package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/documents"
)

func TestMemoToolWriteReadArchiveDelete(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
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
	first := NewMemoTool(&config.MemoConfig{Path: path}, nil)
	if _, err := first.Execute(context.Background(), map[string]any{
		"action": "write", "key": "persistent", "content": "Remember me",
	}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	second := NewMemoTool(&config.MemoConfig{Path: path}, nil)
	result, err := second.Execute(context.Background(), map[string]any{"action": "read", "key": "persistent"})
	if err != nil {
		t.Fatalf("read persisted memo: %v", err)
	}
	if result.(map[string]any)["content"] != "Remember me" {
		t.Errorf("unexpected persisted memo: %#v", result)
	}
}

func TestMemoToolSearchFallsBackToSubstringWithoutEmbeddings(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "fridge", "content": "холодильник надо почистить"}); err != nil {
		t.Fatalf("write fridge memo: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "flight", "content": "купить билеты на самолет"}); err != nil {
		t.Fatalf("write flight memo: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "холодильник"})
	if err != nil {
		t.Fatalf("search memos: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != 1 {
		t.Fatalf("count = %v, want 1", view["count"])
	}
	memos := view["results"].([]map[string]any)
	if memos[0]["key"] != "fridge" {
		t.Errorf("matched memo = %#v, want fridge", memos[0])
	}
}

func TestMemoToolSearchRanksBySemanticSimilarity(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, &config.EmbeddingsConfig{
		BaseURL: "http://embed.test/v1",
		Model:   "embed",
	})
	tool.embed.SetTransport(weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Input string `json:"input"`
		}
		raw, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode embeddings request: %v", err)
		}
		var vector []float64
		switch {
		case strings.Contains(body.Input, "fridge note"):
			vector = []float64{1, 0}
		case strings.Contains(body.Input, "flight note"):
			vector = []float64{0, 1}
		default: // the search query itself
			vector = []float64{0.9, 0.1}
		}
		payload, _ := json.Marshal(map[string]any{"data": []map[string]any{{"embedding": vector}}})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(payload))),
		}, nil
	}))
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "fridge", "content": "fridge note"}); err != nil {
		t.Fatalf("write fridge memo: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "flight", "content": "flight note"}); err != nil {
		t.Fatalf("write flight memo: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "cold storage", "limit": float64(1)})
	if err != nil {
		t.Fatalf("search memos: %v", err)
	}
	view := result.(map[string]any)
	memos := view["results"].([]map[string]any)
	if len(memos) != 1 || memos[0]["key"] != "fridge" {
		t.Fatalf("top match = %#v, want fridge", memos)
	}
}

func TestMemoToolSearchMergesDocumentResults(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	tool.SetDocumentStore(docStore)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "shopping", "content": "Buy milk"}); err != nil {
		t.Fatalf("write memo: %v", err)
	}
	if _, err := docStore.Add(ctx, "Car manual", "Fuse 12 controls the headlights."); err != nil {
		t.Fatalf("add document: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "headlights"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	view := result.(map[string]any)
	results := view["results"].([]map[string]any)
	if len(results) != 1 || results[0]["source"] != "document" || results[0]["document_title"] != "Car manual" {
		t.Fatalf("results = %#v, want a single document match", results)
	}
}
