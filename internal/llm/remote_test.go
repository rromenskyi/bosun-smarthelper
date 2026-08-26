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
		`data: {"model":"text","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}` + "\n" +
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
	client.streamClient.Transport = transport

	var deltas []StreamDelta
	response, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "weather?"}}, nil,
		func(d StreamDelta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if !requestBody.Stream {
		t.Error("request did not ask for streaming")
	}
	if requestBody.StreamOptions == nil || !requestBody.StreamOptions.IncludeUsage {
		t.Error("request did not ask for stream_options.include_usage — the response would never carry real token counts")
	}
	if response.Content != "Сейчас 22.5°C." {
		t.Errorf("content = %q", response.Content)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %d, want 2", len(deltas))
	}
	wantUsage := Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}
	if response.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", response.Usage, wantUsage)
	}
}

// slowBodyReader trickles out fixed chunks with a delay between each,
// simulating a real SSE stream where total duration grows with answer
// length rather than arriving all at once.
type slowBodyReader struct {
	chunks [][]byte
	delay  time.Duration
}

func (r *slowBodyReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func (r *slowBodyReader) Close() error { return nil }

// TestRemoteClientChatStreamOutlivesConfiguredTimeout guards against
// regressing the streamClient split: llm.remote.timeout bounds a single
// blocking Chat() round trip, but http.Client.Timeout covers the entire
// body read — reusing that same bounded client for ChatStream would
// truncate a legitimately slow-but-progressing answer mid-stream well
// before the caller's own (much larger) per-request deadline.
func TestRemoteClientChatStreamOutlivesConfiguredTimeout(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_REMOTE_SLOW_KEY"
	t.Setenv(keyEnv, "remote-secret")

	lines := [][]byte{
		[]byte(`data: {"model":"text","choices":[{"delta":{"content":"one "}}]}` + "\n"),
		[]byte(`data: {"model":"text","choices":[{"delta":{"content":"two "}}]}` + "\n"),
		[]byte(`data: {"model":"text","choices":[{"delta":{"content":"three"}}]}` + "\n"),
		[]byte("data: [DONE]\n"),
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &slowBodyReader{chunks: lines, delay: 30 * time.Millisecond},
		}, nil
	})

	// llm.remote.timeout is 50ms — well under the ~120ms this stream takes
	// to fully arrive. Only streamClient (no Client.Timeout) makes that
	// survivable.
	client, err := NewRemoteClient("https://remote.test/v1", "text", keyEnv, "", 0.8, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.streamClient.Transport = transport

	response, err := client.ChatStream(context.Background(), []Message{{Role: "user", Content: "weather?"}}, nil, func(StreamDelta) {})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v, want the slow stream to survive past the 50ms Chat() timeout", err)
	}
	if response.Content != "one two three" {
		t.Errorf("content = %q", response.Content)
	}
}

func TestRemoteClientSetTemperatureAppliesToNextRequest(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_REMOTE_TEMP_KEY"
	t.Setenv(keyEnv, "remote-secret")

	var gotTemperature float64
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotTemperature = body.Temperature
		return jsonResponse(`{"model":"text","choices":[{"message":{"role":"assistant","content":"ok"}}]}`), nil
	})

	client, err := NewRemoteClient("https://remote.test/v1", "text", keyEnv, "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	if _, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotTemperature != 0.8 {
		t.Fatalf("initial temperature = %v, want 0.8", gotTemperature)
	}

	client.SetTemperature(0.2)
	if _, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat after SetTemperature: %v", err)
	}
	if gotTemperature != 0.2 {
		t.Errorf("temperature after SetTemperature = %v, want 0.2", gotTemperature)
	}
}
