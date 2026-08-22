package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/llm"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

func TestServerMetricMergesEmptyWithoutMemoTool(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	request := httptest.NewRequest(http.MethodGet, "/api/metric-merges", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	suggestions, _ := body["suggestions"].([]any)
	if len(suggestions) != 0 {
		t.Errorf("suggestions = %v, want empty without a wired memo tool", suggestions)
	}

	decideRequest := httptest.NewRequest(http.MethodPost, "/api/metric-merges/anything/decide", bytes.NewReader([]byte(`{"approve":true}`)))
	decideResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(decideResponse, decideRequest)
	if decideResponse.Code != http.StatusNotFound {
		t.Errorf("decide status = %d, want 404 without a wired memo tool", decideResponse.Code)
	}
}

func TestServerMetricMergesListAndDecide(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	memoTool := tools.NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	server.SetMemoTool(memoTool)
	ctx := context.Background()

	if _, err := memoTool.Execute(ctx, map[string]any{
		"action": "write", "key": "a", "content": "a", "metric_name": "name_a", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := memoTool.Execute(ctx, map[string]any{
		"action": "write", "key": "b", "content": "b", "metric_name": "name_b", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := memoTool.CheckMetricMerges(ctx, &fakeMergeChatClient{response: "1: yes, name_a"}, 10); err != nil {
		t.Fatalf("CheckMetricMerges: %v", err)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/metric-merges", nil)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	var listBody struct {
		Suggestions []tools.MetricMergeSuggestion `json:"suggestions"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listBody.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want exactly 1", listBody.Suggestions)
	}
	id := listBody.Suggestions[0].ID

	decideRequest := httptest.NewRequest(http.MethodPost, "/api/metric-merges/"+id+"/decide", bytes.NewReader([]byte(`{"approve":true}`)))
	decideResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(decideResponse, decideRequest)
	if decideResponse.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body = %s", decideResponse.Code, decideResponse.Body.String())
	}
	var decided tools.MetricMergeSuggestion
	if err := json.NewDecoder(decideResponse.Body).Decode(&decided); err != nil {
		t.Fatalf("decode decide response: %v", err)
	}
	if decided.Status != "approved" {
		t.Errorf("status = %q, want approved", decided.Status)
	}

	// The queue must be empty again — the suggestion is no longer pending.
	afterRequest := httptest.NewRequest(http.MethodGet, "/api/metric-merges", nil)
	afterResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(afterResponse, afterRequest)
	var afterBody struct {
		Suggestions []tools.MetricMergeSuggestion `json:"suggestions"`
	}
	if err := json.NewDecoder(afterResponse.Body).Decode(&afterBody); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if len(afterBody.Suggestions) != 0 {
		t.Errorf("suggestions after approval = %+v, want none", afterBody.Suggestions)
	}
}

func TestServerMetricMergeDecideUnknownIDReturnsBadRequest(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	memoTool := tools.NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	server.SetMemoTool(memoTool)

	request := httptest.NewRequest(http.MethodPost, "/api/metric-merges/does-not-exist/decide", bytes.NewReader([]byte(`{"approve":true}`)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown suggestion id", response.Code)
	}
}

// fakeMergeChatClient satisfies MemoTool.CheckMetricMerges' chatClient
// parameter structurally — that interface is unexported, but Go interface
// satisfaction only depends on the method set, not the interface's name or
// export status, so this compiles and works fine from another package.
type fakeMergeChatClient struct {
	response string
}

func (f *fakeMergeChatClient) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolDefinition) (*llm.Response, error) {
	return &llm.Response{Content: f.response}, nil
}
