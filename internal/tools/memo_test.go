package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/documents"
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

// TestMemoToolSearchCapsLimitRegardlessOfRequestedValue is a regression
// test for a real incident: a model-supplied limit had no upper bound,
// and after the document store grew large a single search call with an
// unusually large limit produced a request too big for a local model's
// context window (see maxSearchLimit's doc comment).
func TestMemoToolSearchCapsLimitRegardlessOfRequestedValue(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	for i := 0; i < maxSearchLimit+10; i++ {
		key := fmt.Sprintf("note-%d", i)
		if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": key, "content": "жара летом"}); err != nil {
			t.Fatalf("write memo %d: %v", i, err)
		}
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "жара", "limit": float64(1000)})
	if err != nil {
		t.Fatalf("search memos: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != maxSearchLimit {
		t.Fatalf("count = %v, want capped at %d despite requesting 1000", view["count"], maxSearchLimit)
	}
	memos := view["results"].([]map[string]any)
	if len(memos) != maxSearchLimit {
		t.Errorf("results = %d, want capped at %d", len(memos), maxSearchLimit)
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

func TestMemoToolSearchFiltersLowRelevance(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{
		Path:               filepath.Join(t.TempDir(), "memos.json"),
		MinSearchRelevance: 0.5,
	}, &config.EmbeddingsConfig{
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
			vector = []float64{1, 0} // orthogonal to the query below — cosine ~0, filtered out
		case strings.Contains(body.Input, "flight note"):
			vector = []float64{0.1, 0.99} // near-identical direction to the query — high relevance
		default: // the search query itself
			vector = []float64{0, 1}
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

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "test query"})
	if err != nil {
		t.Fatalf("search memos: %v", err)
	}
	view := result.(map[string]any)
	memos := view["results"].([]map[string]any)
	if len(memos) != 1 || memos[0]["key"] != "flight" {
		t.Fatalf("results = %#v, want only the high-relevance flight memo", memos)
	}
}

func TestMemoToolSearchTruncatesLongContent(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	longContent := "garage door code " + strings.Repeat("blah ", 200) // well over maxSearchResultChars
	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "notes", "content": longContent}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "garage door code"})
	if err != nil {
		t.Fatalf("search memos: %v", err)
	}
	memos := result.(map[string]any)["results"].([]map[string]any)
	if len(memos) != 1 {
		t.Fatalf("results = %#v, want 1 match", memos)
	}
	content := memos[0]["content"].(string)
	if runeLen := len([]rune(content)); runeLen > maxSearchResultChars+1 {
		t.Errorf("search result content is %d runes, want <= %d (truncated)", runeLen, maxSearchResultChars+1)
	}
	if !strings.HasSuffix(content, "…") {
		t.Errorf("truncated content = %q, want an ellipsis suffix", content)
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
	if _, err := docStore.Add(ctx, "Car manual", "Fuse 12 controls the headlights.", "docs/ford/generator-repair"); err != nil {
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
	if results[0]["source_path"] != "docs/ford/generator-repair" {
		t.Errorf("source_path = %v, want docs/ford/generator-repair", results[0]["source_path"])
	}
}

// TestMemoToolSearchDocumentResultKeepsContentPastOldTruncationPoint is a
// regression test for a real incident: a document chunk's useful content
// (a fuse-panel "which fuse protects what" table) sat right after ~500
// characters of unrelated preamble in the same OCR'd page. The old
// shared 500-char truncation cut it off before the model ever saw it,
// so it answered as if the manual simply didn't have the table. Document
// chunks now get the more generous maxDocumentResultChars instead.
func TestMemoToolSearchDocumentResultKeepsContentPastOldTruncationPoint(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	tool.SetDocumentStore(docStore)
	ctx := context.Background()

	preamble := strings.Repeat("filler ", 90) // ~630 chars, past the old 500-char cap
	content := preamble + "Fuse Position 12 Circuits Protected: Dome Lamp, Map Lamp, Radio Memory."
	if len(content) <= maxSearchResultChars {
		t.Fatalf("test content is %d chars, want it longer than the old cap (%d) to actually exercise this", len(content), maxSearchResultChars)
	}
	if len(content) > maxDocumentResultChars {
		t.Fatalf("test content is %d chars, want it under maxDocumentResultChars (%d) so nothing gets cut", len(content), maxDocumentResultChars)
	}
	if _, err := docStore.Add(ctx, "Fuse Panel", content, "ford-e350/diagrams"); err != nil {
		t.Fatalf("add document: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "dome lamp"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	view := result.(map[string]any)
	results := view["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("results = %#v, want 1 match", results)
	}
	text, _ := results[0]["text"].(string)
	if !strings.Contains(text, "Dome Lamp") {
		t.Errorf("result text = %q, want the fuse table past the old 500-char cutoff to still be present", text)
	}
}

func TestMemoToolSearchDocumentResultOmitsSourcePathWhenEmpty(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	tool.SetDocumentStore(docStore)
	ctx := context.Background()

	if _, err := docStore.Add(ctx, "Car manual", "Fuse 12 controls the headlights.", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "headlights"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	view := result.(map[string]any)
	results := view["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("results = %#v, want 1 match", results)
	}
	if _, ok := results[0]["source_path"]; ok {
		t.Errorf("results[0] = %#v, want no source_path key for a legacy document with no source", results[0])
	}
}

func TestMemoToolTopicsListsUploadedDocuments(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	tool.SetDocumentStore(docStore)
	ctx := context.Background()

	summary, err := docStore.Add(ctx, "Car manual", "Fuse 12 controls the headlights.", "")
	if err != nil {
		t.Fatalf("add document: %v", err)
	}
	// A memo must never show up in topics — that's what "list" is for.
	if _, err := tool.Execute(ctx, map[string]any{"action": "write", "key": "shopping", "content": "Buy milk"}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "topics"})
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	view := result.(map[string]any)
	topics := view["documents"].([]map[string]any)
	if len(topics) != 1 || topics[0]["document_id"] != summary.ID || topics[0]["title"] != "Car manual" {
		t.Fatalf("topics = %#v, want just the Car manual document", topics)
	}
}

// TestMemoToolTopicsCapsResultsRegardlessOfStoreSize is a regression test
// for a real incident: topics() returned every uploaded document with no
// cap at all (no limit argument exists for it), and after a bulk manual
// import grew the document store past ~1000 entries, a single topics
// call was by itself large enough to contribute to a local model's
// context overflow.
func TestMemoToolTopicsCapsResultsRegardlessOfStoreSize(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	tool.SetDocumentStore(docStore)
	ctx := context.Background()

	for i := 0; i < maxTopicsListed+10; i++ {
		title := fmt.Sprintf("Manual %d", i)
		if _, err := docStore.Add(ctx, title, "some content", ""); err != nil {
			t.Fatalf("add document %d: %v", i, err)
		}
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "topics"})
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != maxTopicsListed+10 {
		t.Errorf("count = %v, want the true total %d", view["count"], maxTopicsListed+10)
	}
	topics := view["documents"].([]map[string]any)
	if len(topics) != maxTopicsListed {
		t.Fatalf("documents = %d entries, want capped at %d", len(topics), maxTopicsListed)
	}
	if view["note"] == nil {
		t.Error("expected a note explaining the list was truncated")
	}
}

func TestMemoToolTopicsWithoutDocumentStoreReturnsEmpty(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	result, err := tool.Execute(context.Background(), map[string]any{"action": "topics"})
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != 0 {
		t.Errorf("count = %v, want 0 when no document store is configured", view["count"])
	}
}

func TestMemoToolSearchScopesToDocumentID(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	docStore := documents.NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	tool.SetDocumentStore(docStore)
	ctx := context.Background()

	carSummary, err := docStore.Add(ctx, "Car manual", "the headlight fuse is number 12", "")
	if err != nil {
		t.Fatalf("add car manual: %v", err)
	}
	if _, err := docStore.Add(ctx, "Boat manual", "the headlight fuse is number 3", ""); err != nil {
		t.Fatalf("add boat manual: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{
		"action": "search", "query": "headlight fuse", "document_id": carSummary.ID,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	view := result.(map[string]any)
	results := view["results"].([]map[string]any)
	if len(results) != 1 || results[0]["document_title"] != "Car manual" {
		t.Fatalf("results = %#v, want only the Car manual match", results)
	}
}

func TestMemoToolWriteWithTagsAndFilter(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	written, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil today",
		"tags": []any{"maintenance", "oil"},
	})
	if err != nil {
		t.Fatalf("write memo: %v", err)
	}
	tags, _ := written.(map[string]any)["tags"].([]string)
	if len(tags) != 2 || tags[0] != "maintenance" || tags[1] != "oil" {
		t.Errorf("tags = %#v, want [maintenance oil]", written.(map[string]any)["tags"])
	}

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "groceries", "content": "Bought supplies",
		"tags": []any{"purchases"},
	}); err != nil {
		t.Fatalf("write memo: %v", err)
	}

	// Omitting "tags" on a re-write must not clear existing tags.
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil today, again",
	}); err != nil {
		t.Fatalf("re-write memo: %v", err)
	}
	read, err := tool.Execute(ctx, map[string]any{"action": "read", "key": "oil-change"})
	if err != nil {
		t.Fatalf("read memo: %v", err)
	}
	if tags, _ := read.(map[string]any)["tags"].([]string); len(tags) != 2 {
		t.Errorf("tags after re-write without tags param = %#v, want unchanged", read.(map[string]any)["tags"])
	}

	listResult, err := tool.Execute(ctx, map[string]any{"action": "list", "tag": "purchases"})
	if err != nil {
		t.Fatalf("list with tag filter: %v", err)
	}
	listView := listResult.(map[string]any)
	memos := listView["memos"].([]map[string]any)
	if len(memos) != 1 || memos[0]["key"] != "groceries" {
		t.Fatalf("tag-filtered list = %#v, want only groceries", memos)
	}

	// Tag filter narrows the substring-fallback search too.
	searchResult, err := tool.Execute(ctx, map[string]any{"action": "search", "query": "oil", "tag": "purchases"})
	if err != nil {
		t.Fatalf("search with tag filter: %v", err)
	}
	searchView := searchResult.(map[string]any)
	if searchView["count"] != 0 {
		t.Errorf("tag-filtered search count = %v, want 0 (oil-change isn't tagged purchases)", searchView["count"])
	}
}

func TestMemoToolWriteStoresMaintenanceFields(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"metric_name": "odometer_km", "metric_value": 55000.0,
		"due_date": "2028-08-20", "due_metric_value": 65000.0,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	view := result.(map[string]any)
	if view["metric_name"] != "odometer_km" || view["metric_value"] != 55000.0 {
		t.Errorf("view = %+v, want metric_name/metric_value stored", view)
	}
	if view["due_metric_value"] != 65000.0 {
		t.Errorf("view = %+v, want due_metric_value stored", view)
	}
	dueDate, _ := view["due_date"].(string)
	if !strings.HasPrefix(dueDate, "2028-08-20") {
		t.Errorf("due_date = %q, want it to start with 2028-08-20", dueDate)
	}
}

func TestMemoToolWriteRejectsInvalidDueDate(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"due_date": "next tuesday",
	}); err == nil {
		t.Error("expected an error for an unparseable due_date")
	}
}

func TestMemoToolMaintenanceReportsOverdueAndUpcoming(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	past := time.Now().Add(-48 * time.Hour).Format("2006-01-02")
	future := time.Now().Add(240 * time.Hour).Format("2006-01-02")
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "coolant-check", "content": "Checked coolant", "due_date": past,
	}); err != nil {
		t.Fatalf("write overdue item: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "belt-check", "content": "Checked belt", "due_date": future,
	}); err != nil {
		t.Fatalf("write upcoming item: %v", err)
	}
	// A plain memo with no due fields at all must never show up here.
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "shopping", "content": "Buy milk",
	}); err != nil {
		t.Fatalf("write unrelated memo: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "maintenance"})
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	items := view["items"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("items = %+v, want exactly the 2 due-tracked memos", items)
	}
	byKey := map[string]map[string]any{}
	for _, item := range items {
		byKey[item["key"].(string)] = item
	}
	if byKey["coolant-check"]["overdue"] != true {
		t.Errorf("coolant-check overdue = %v, want true", byKey["coolant-check"]["overdue"])
	}
	if byKey["belt-check"]["overdue"] != false {
		t.Errorf("belt-check overdue = %v, want false", byKey["belt-check"]["overdue"])
	}
	daysUntil, _ := byKey["belt-check"]["days_until_due"].(int)
	if daysUntil < 9 || daysUntil > 10 {
		t.Errorf("belt-check days_until_due = %v, want roughly 10", daysUntil)
	}
}

func TestMemoToolMaintenanceComputesRemainingFromLatestReading(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"metric_name": "odometer_km", "metric_value": 55000.0, "due_metric_value": 65000.0,
	}); err != nil {
		t.Fatalf("write maintenance item: %v", err)
	}
	// A later, unrelated memo that just happens to mention the current
	// reading — this is the only "sensor" this mechanism has.
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "odometer-reading", "content": "Current odometer",
		"metric_name": "odometer_km", "metric_value": 61000.0,
	}); err != nil {
		t.Fatalf("write reading: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "maintenance"})
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	items := view["items"].([]map[string]any)
	var oilChange map[string]any
	for _, item := range items {
		if item["key"] == "oil-change" {
			oilChange = item
		}
	}
	if oilChange == nil {
		t.Fatalf("items = %+v, missing oil-change", items)
	}
	if oilChange["latest_known_metric_value"] != 61000.0 {
		t.Errorf("latest_known_metric_value = %v, want 61000", oilChange["latest_known_metric_value"])
	}
	if oilChange["remaining_metric_value"] != 4000.0 {
		t.Errorf("remaining_metric_value = %v, want 4000 (65000-61000)", oilChange["remaining_metric_value"])
	}

	metrics, _ := view["known_metrics"].([]string)
	if len(metrics) != 1 || metrics[0] != "odometer_km" {
		t.Errorf("known_metrics = %v, want just odometer_km", metrics)
	}
}

func TestMemoToolMaintenanceExcludesArchivedMemos(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "old-item", "content": "Old maintenance item",
		"due_date": time.Now().Add(-48 * time.Hour).Format("2006-01-02"),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "archive", "key": "old-item"}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "maintenance"})
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != 0 {
		t.Errorf("count = %v, want 0 — the only due item is archived", view["count"])
	}
}

func TestMemoToolWriteFlagsUnrecognizedMetricName(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"metric_name": "odometer_km", "metric_value": 55000.0,
	}); err != nil {
		t.Fatalf("write first record: %v", err)
	}

	// A slightly different spelling for the same counter — write's own
	// response must flag this immediately, in time for the model to
	// correct it within the same turn, rather than silently fragmenting
	// into two unrelated counters.
	result, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "odometer-reading", "content": "Current odometer",
		"metric_name": "odometer", "metric_value": 61000.0,
	})
	if err != nil {
		t.Fatalf("write second record: %v", err)
	}
	view := result.(map[string]any)
	existing, _ := view["existing_metric_names"].([]string)
	if len(existing) != 1 || existing[0] != "odometer_km" {
		t.Errorf("existing_metric_names = %v, want [odometer_km]", existing)
	}
}

func TestMemoToolWriteReusingKnownMetricNameOmitsHint(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"metric_name": "odometer_km", "metric_value": 55000.0,
	}); err != nil {
		t.Fatalf("write first record: %v", err)
	}
	result, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "odometer-reading", "content": "Current odometer",
		"metric_name": "odometer_km", "metric_value": 61000.0,
	})
	if err != nil {
		t.Fatalf("write second record: %v", err)
	}
	view := result.(map[string]any)
	if _, ok := view["existing_metric_names"]; ok {
		t.Errorf("existing_metric_names = %v, want absent — the name matches an existing one", view["existing_metric_names"])
	}
}

func TestMemoToolWriteRejectsDueMetricValueWithoutMetricName(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"due_metric_value": 65000.0,
	}); err == nil {
		t.Error("expected an error for due_metric_value with no metric_name — there's no counter to compare it against")
	}
}

func TestMemoToolWriteAllowsDueMetricValueWhenMetricNameAlreadySet(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil",
		"metric_name": "odometer_km", "metric_value": 55000.0,
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// due_metric_value alone, on a later write to the same key — metric_name
	// carries over from the existing record, so this must not be rejected.
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "Changed the oil, due noted",
		"due_metric_value": 65000.0,
	}); err != nil {
		t.Errorf("write with due_metric_value only should succeed since metric_name was already set: %v", err)
	}
}

func TestMemoToolMaintenanceIgnoresMalformedDueMetricValueRecord(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	// write rejects due_metric_value without metric_name, but maintenance
	// must not surface a bare, unexplained "due" item for one anyway — e.g.
	// a record written before that check existed, or edited by hand.
	data := memoFile{Memos: map[string]memoRecord{
		"malformed": {
			Key: "malformed", Content: "no metric_name", Status: "active",
			DueMetricValue: 65000, UpdatedAt: time.Now().Format(time.RFC3339),
		},
	}}
	result, err := tool.maintenance(data, time.Now())
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	if view["count"] != 0 {
		t.Errorf("count = %v, want 0 — a due_metric_value with no metric_name isn't a real due item", view["count"])
	}
}

func TestMemoToolMaintenanceLatestReadingSurvivesUTCOffsetChange(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	// A naive string comparison of UpdatedAt breaks across a UTC offset
	// change (e.g. a DST transition): "01:30:00-05:00" sorts after
	// "01:15:00-06:00" lexicographically, even though -06:00 is the later
	// moment in real time (07:15 UTC vs 06:30 UTC).
	data := memoFile{Memos: map[string]memoRecord{
		"oil-change": {
			Key: "oil-change", Content: "changed the oil", Status: "active",
			MetricName: "odometer_km", DueMetricValue: 65000,
			UpdatedAt: "2026-01-01T00:00:00Z",
		},
		"reading-earlier-offset": {
			Key: "reading-earlier-offset", Content: "current odometer", Status: "active",
			MetricName: "odometer_km", MetricValue: 55000,
			UpdatedAt: "2026-11-01T01:30:00-05:00", // 06:30 UTC
		},
		"reading-later-offset": {
			Key: "reading-later-offset", Content: "current odometer", Status: "active",
			MetricName: "odometer_km", MetricValue: 61000,
			UpdatedAt: "2026-11-01T01:15:00-06:00", // 07:15 UTC — actually later
		},
	}}

	result, err := tool.maintenance(data, time.Now())
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	items := view["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly the 1 due-tracked memo", items)
	}
	if items[0]["latest_known_metric_value"] != 61000.0 {
		t.Errorf("latest_known_metric_value = %v, want 61000 (the chronologically later reading, despite its lexicographically smaller UpdatedAt)", items[0]["latest_known_metric_value"])
	}
}
