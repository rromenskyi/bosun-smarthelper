package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestLocalClientOllamaToolCallRoundTrip(t *testing.T) {
	var requests []ollamaRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}

		var request ollamaRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, request)

		if len(requests) == 1 {
			return jsonResponse(`{
				"model":"test-model",
				"message":{"role":"assistant","content":"","tool_calls":[{
					"function":{"name":"get_weather","arguments":{"location":"Denver"}}
				}]},
				"done":true
			}`), nil
		}
		return jsonResponse(`{
			"model":"test-model",
			"message":{"role":"assistant","content":"It is 21 C."},
			"done":true
		}`), nil
	})

	client := NewLocalClient("http://ollama.test/", "test-model", 0.5, time.Second, true)
	client.client.Transport = transport
	first, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "weather?"}}, []ToolDefinition{{
		Name:       "get_weather",
		Parameters: map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatalf("first Chat returned error: %v", err)
	}
	if got := first.ToolCalls[0].Function.Arguments; got != `{"location":"Denver"}` {
		t.Fatalf("arguments = %q, want JSON object encoded as a string", got)
	}
	if got := requests[0].Options["temperature"]; got != 0.5 {
		t.Errorf("options.temperature = %v, want 0.5", got)
	}

	toolCall := first.ToolCalls[0]
	_, err = client.Chat(context.Background(), []Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: []ToolCall{toolCall}},
		{Role: "tool", Name: "get_weather", ToolCallID: toolCall.ID, Content: `{"temperature_c":21}`},
	}, nil)
	if err != nil {
		t.Fatalf("second Chat returned error: %v", err)
	}

	assistant := requests[1].Messages[1]
	if got := string(assistant.ToolCalls[0].Function.Arguments); got != `{"location":"Denver"}` {
		t.Errorf("follow-up arguments = %s, want JSON object", got)
	}
	if got := requests[1].Messages[2].ToolName; got != "get_weather" {
		t.Errorf("tool_name = %q, want get_weather", got)
	}
}

func TestOpenAICompatibleLocalClient(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_LOCAL_KEY"
	t.Setenv(keyEnv, "local-secret")

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer local-secret" {
			t.Errorf("Authorization = %q", got)
		}
		return jsonResponse(`{
			"model":"default",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`), nil
	})

	client, err := NewOpenAICompatibleLocalClient("http://lm-studio.test/v1/", "default", keyEnv, 0.5, time.Second, true)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport
	response, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if response.Content != "hello" {
		t.Errorf("content = %q, want hello", response.Content)
	}
	if client.Provider() != "local" {
		t.Errorf("provider = %q, want local", client.Provider())
	}
}

func TestOpenAICompatibleLocalClientPromptedToolFallback(t *testing.T) {
	callCount := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		callCount++
		var request openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(request.Tools) != 0 {
			t.Errorf("native tools sent to an unsupported server: %d", len(request.Tools))
		}
		if callCount == 1 {
			return jsonResponse(`{
				"model":"default",
				"choices":[{"message":{"role":"assistant","content":"{\"tool\":\"get_weather\",\"arguments\":{}}"}}]
			}`), nil
		}
		return jsonResponse(`{
			"model":"default",
			"choices":[{"message":{"role":"assistant","content":"It is 22.5 C."}}]
		}`), nil
	})

	client, err := NewOpenAICompatibleLocalClient("http://lm-studio.test/v1", "default", "", 0.5, time.Second, true)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport
	client.supportsTools = false
	definitions := []ToolDefinition{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}}

	first, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "weather?"}}, definitions)
	if err != nil {
		t.Fatalf("first Chat returned error: %v", err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected prompted tool call: %+v", first.ToolCalls)
	}

	second, err := client.Chat(context.Background(), []Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: first.ToolCalls},
		{Role: "tool", Name: "get_weather", Content: `{"temperature_c":22.5}`},
	}, definitions)
	if err != nil {
		t.Fatalf("second Chat returned error: %v", err)
	}
	if second.Content != "It is 22.5 C." {
		t.Errorf("content = %q", second.Content)
	}
}

func TestOpenAICompatibleLocalClientRecognizesToolMention(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{
			"model":"default",
			"choices":[{"message":{"role":"assistant","content":"I need to call get_weather."}}]
		}`), nil
	})

	client, err := NewOpenAICompatibleLocalClient("http://lm-studio.test/v1", "default", "", 0.5, time.Second, true)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport
	client.supportsTools = false

	response, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "weather?"}}, []ToolDefinition{
		{Name: "get_weather", Parameters: map[string]any{"type": "object"}},
		{Name: "get_gps", Parameters: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected tool calls: %+v", response.ToolCalls)
	}
}

func TestOpenAICompatibleLocalClientRequiresConfiguredKey(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_MISSING_LOCAL_KEY"
	t.Setenv(keyEnv, "")
	if _, err := NewOpenAICompatibleLocalClient("", "", keyEnv, 0.5, time.Second, true); err == nil {
		t.Fatal("expected an error for a missing configured API key")
	}
}

func TestCompactToolDefinitions(t *testing.T) {
	tools := []ToolDefinition{
		{
			Name:        "memo",
			Description: "Write, read, list, archive, or delete persistent local memos.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "enum": []string{"write", "read", "list"}},
					"key":     map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []string{"action"},
			},
		},
		{
			Name:        "get_gps",
			Description: "Get current GPS coordinates.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}

	rendered := compactToolDefinitions(tools)
	lines := strings.Split(rendered, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), rendered)
	}

	memoLine := lines[0]
	if !strings.HasPrefix(memoLine, "memo(action:write|read|list, content?:string, key?:string): ") {
		t.Errorf("memo line = %q", memoLine)
	}
	if !strings.Contains(memoLine, "Write, read, list, archive, or delete persistent local memos.") {
		t.Errorf("memo line missing description: %q", memoLine)
	}

	gpsLine := lines[1]
	if gpsLine != "get_gps(): Get current GPS coordinates." {
		t.Errorf("gps line = %q", gpsLine)
	}

	// Full JSON Schema form should always cost more tokens than the compact one.
	full, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) >= len(full) {
		t.Errorf("compact form (%d bytes) is not smaller than full JSON Schema (%d bytes)", len(rendered), len(full))
	}
}

func TestParseLlamaToolCalls(t *testing.T) {
	response := &Response{Content: `I will check.
<tool_call>
<function=get_weather>
<parameter=location>
Denver
</parameter>
</function>
</tool_call>`}

	parseLlamaToolCalls(response)
	if response.Content != "I will check." {
		t.Errorf("remaining content = %q", response.Content)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.Function.Name != "get_weather" {
		t.Errorf("name = %q", call.Function.Name)
	}
	if call.Function.Arguments != `{"location":"Denver"}` {
		t.Errorf("arguments = %s", call.Function.Arguments)
	}
}

func TestStripLeakedReasoningMarker(t *testing.T) {
	response := &Response{Content: "0thought\n<channel|>Слушаюсь, капитан! Вот ссылки."}
	stripLeakedReasoningMarker(response)
	if response.Content != "Слушаюсь, капитан! Вот ссылки." {
		t.Errorf("content = %q", response.Content)
	}
}

func TestStripLeakedReasoningMarker_NoMarkerLeavesContentUnchanged(t *testing.T) {
	response := &Response{Content: "Normal answer with no leaked marker."}
	stripLeakedReasoningMarker(response)
	if response.Content != "Normal answer with no leaked marker." {
		t.Errorf("content = %q, should be unchanged", response.Content)
	}
}

func TestStripLeakedReasoningMarker_OnlyStripsAtStart(t *testing.T) {
	response := &Response{Content: "The channel|> marker mid-sentence should stay untouched."}
	stripLeakedReasoningMarker(response)
	if response.Content != "The channel|> marker mid-sentence should stay untouched." {
		t.Errorf("content = %q, a mid-sentence occurrence must not be stripped", response.Content)
	}
}

func TestLocalClientChatStreamOllama(t *testing.T) {
	var requests []ollamaRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request ollamaRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, request)

		body := `{"model":"test-model","message":{"role":"assistant","content":"Сейчас "},"done":false}` + "\n" +
			`{"model":"test-model","message":{"role":"assistant","content":"22.5°C."},"done":false}` + "\n" +
			`{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"eval_count":4,"prompt_eval_count":10}` + "\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})

	client := NewLocalClient("http://ollama.test/", "test-model", 0.5, time.Second, true)
	client.client.Transport = transport

	var deltas []StreamDelta
	response, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "weather?"}}, nil,
		func(d StreamDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if !requests[0].Stream {
		t.Error("request did not ask for streaming")
	}
	if response.Content != "Сейчас 22.5°C." {
		t.Errorf("content = %q", response.Content)
	}
	if response.Usage.TotalTokens != 14 {
		t.Errorf("total tokens = %d, want 14", response.Usage.TotalTokens)
	}
	for _, d := range deltas {
		if d.Kind != "prose" {
			t.Errorf("unexpected delta kind %q — Ollama tool_calls are structured, never fold", d.Kind)
		}
	}
}

func TestLocalClientChatStreamOpenAI_FoldsToolCallXML(t *testing.T) {
	// Same shape as the real llama-server --skip-chat-parsing output this
	// host actually runs: narration, then a tool call embedded in content.
	sse := `data: {"model":"default","choices":[{"delta":{"content":"I will check.\n<tool_call>\n<function=get_weather>\n<parameter=location>\nDenver\n</parameter>\n</function>\n</tool_call>"}}]}` + "\n" +
		"data: [DONE]\n"

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	})

	client, err := NewOpenAICompatibleLocalClient("http://lm-studio.test/v1", "default", "", 0.5, time.Second, true)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	var prose, fold strings.Builder
	response, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "weather?"}}, []ToolDefinition{{
		Name:       "get_weather",
		Parameters: map[string]any{"type": "object"},
	}}, func(d StreamDelta) {
		switch d.Kind {
		case "prose":
			prose.WriteString(d.Text)
		case "fold":
			fold.WriteString(d.Text)
		}
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if prose.String() != "I will check.\n" {
		t.Errorf("prose = %q", prose.String())
	}
	if !strings.Contains(fold.String(), "<tool_call>") {
		t.Errorf("fold = %q, want the raw tool-call XML", fold.String())
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected tool calls: %+v", response.ToolCalls)
	}
	if response.Content != "I will check." {
		t.Errorf("content = %q, want the tool-call XML stripped same as the non-streaming path", response.Content)
	}
}

func TestLocalClientChatStreamDisabledFallsBackToBuffered(t *testing.T) {
	// Some llama.cpp builds/models corrupt multi-byte UTF-8 in streaming
	// mode; stream:false must route ChatStream through the buffered Chat
	// path and still emit exactly one prose delta with the full answer.
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("stream") == "true" {
			t.Error("streaming request sent despite streamEnabled=false")
		}
		return jsonResponse(`{
			"model":"default",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Сейчас 22.5°C."},"finish_reason":"stop"}]
		}`), nil
	})

	client, err := NewOpenAICompatibleLocalClient("http://lm-studio.test/v1", "default", "", 0.5, time.Second, false)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	var deltas []StreamDelta
	response, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "weather?"}}, nil,
		func(d StreamDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if response.Content != "Сейчас 22.5°C." {
		t.Errorf("content = %q", response.Content)
	}
	if len(deltas) != 1 || deltas[0].Kind != "prose" || deltas[0].Text != "Сейчас 22.5°C." {
		t.Errorf("deltas = %+v, want exactly one prose delta with the full content", deltas)
	}
}
