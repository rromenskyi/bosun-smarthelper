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

func TestRemoteClientChat(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_REMOTE_KEY"
	t.Setenv(keyEnv, "remote-secret")

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer remote-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("OpenAI-Organization"); got != "org-test" {
			t.Errorf("OpenAI-Organization = %q", got)
		}

		var request openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.ToolChoice != "auto" || len(request.Tools) != 1 {
			t.Errorf("tool configuration = choice %q, tools %d", request.ToolChoice, len(request.Tools))
		}
		if request.Temperature != 0.8 {
			t.Errorf("temperature = %v, want 0.8", request.Temperature)
		}

		return jsonResponse(`{
			"model":"text",
			"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{
				"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}
			}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
		}`), nil
	})

	client, err := NewRemoteClient("https://remote.test/v1/", "text", keyEnv, "org-test", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport
	response, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "weather?"}}, []ToolDefinition{{
		Name:       "get_weather",
		Parameters: map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected tool calls: %+v", response.ToolCalls)
	}
}

func TestRemoteClientChatStream(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_REMOTE_STREAM_KEY"
	t.Setenv(keyEnv, "remote-secret")

	sse := `data: {"model":"text","choices":[{"delta":{"content":"Сейчас "}}]}` + "\n" +
		`data: {"model":"text","choices":[{"delta":{"content":"22.5°C."}}]}` + "\n" +
		"data: [DONE]\n"

	var requestBody openAIRequest
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	})

	client, err := NewRemoteClient("https://remote.test/v1", "text", keyEnv, "", 0.8, time.Second)
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
	if !requestBody.Stream {
		t.Error("request did not ask for streaming")
	}
	if response.Content != "Сейчас 22.5°C." {
		t.Errorf("content = %q", response.Content)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %d, want 2", len(deltas))
	}
}
