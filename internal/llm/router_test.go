package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestRouterLastProvider(t *testing.T) {
	router := &Router{
		localClient: NewLocalClient("", "", 0.5, time.Second, true),
		config:      &config.LLMConfig{},
	}
	if got := router.LastProvider(); got != "local" {
		t.Errorf("initial provider = %q, want local", got)
	}
	router.setLastProvider("remote")
	if got := router.LastProvider(); got != "remote" {
		t.Errorf("last provider = %q, want remote", got)
	}
	if got := router.ActiveProvider(); got != "local" {
		t.Errorf("idle provider = %q, want local", got)
	}
	router.setActiveProvider("remote")
	if got := router.ActiveProvider(); got != "remote" {
		t.Errorf("active provider = %q, want remote", got)
	}
	router.clearActiveProvider()
	if got := router.ActiveProvider(); got != "local" {
		t.Errorf("provider after request = %q, want local", got)
	}
}

func TestRouterRetriesTransientRemoteErrors(t *testing.T) {
	attempts := 0
	client := &RemoteClient{
		baseURL: "https://remote.test/v1",
		model:   "text",
		apiKey:  "test",
		client: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts++
			if attempts < 3 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader(`{"error":"temporary"}`)),
					Header:     make(http.Header),
				}, nil
			}
			return jsonResponse(`{
				"model":"text",
				"choices":[{"message":{"role":"assistant","content":"ok"}}]
			}`), nil
		})},
	}
	router := &Router{remoteMaxRetries: 5, remoteRetryBackoff: time.Millisecond}

	response, err := router.chatRemoteWithRetry(context.Background(), client, []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chatRemoteWithRetry returned error: %v", err)
	}
	if response.Content != "ok" || attempts != 3 {
		t.Errorf("response = %q, attempts = %d", response.Content, attempts)
	}
}

// failingReader returns data once, then err on every subsequent read —
// simulates a connection that streams some bytes before dying.
type failingReader struct {
	data []byte
	pos  int
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

type fakeNetError struct{ msg string }

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return false }
func (e *fakeNetError) Temporary() bool { return true }

func TestRouterChatStream_RetriesBeforeFirstDelta(t *testing.T) {
	attempts := 0
	testClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(`{"error":"temporary"}`)),
				Header:     make(http.Header),
			}, nil
		}
		sse := `data: {"model":"text","choices":[{"delta":{"content":"ok"}}]}` + "\ndata: [DONE]\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	})}
	remote := &RemoteClient{
		baseURL: "https://remote.test/v1", model: "text", apiKey: "test",
		client:       testClient,
		streamClient: testClient,
	}
	router := &Router{
		remoteClient:       remote,
		config:             &config.LLMConfig{Router: config.RouterConfig{PreferRemote: true}},
		isOnline:           true,
		lastCheck:          time.Now(),
		remoteMaxRetries:   5,
		remoteRetryBackoff: time.Millisecond,
	}

	response, err := router.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (retry works normally before any delta)", attempts)
	}
	if response.Content != "ok" {
		t.Errorf("content = %q", response.Content)
	}
}

func TestRouterChatStream_NoRetryOrFallbackAfterDeltaSent(t *testing.T) {
	attempts := 0
	sseFirstLine := `data: {"model":"text","choices":[{"delta":{"content":"partial"}}]}` + "\n"
	testClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(&failingReader{data: []byte(sseFirstLine), err: &fakeNetError{msg: "connection reset"}}),
		}, nil
	})}
	remote := &RemoteClient{
		baseURL: "https://remote.test/v1", model: "text", apiKey: "test",
		client:       testClient,
		streamClient: testClient,
	}
	local := NewLocalClient("http://ollama.test/", "test-model", 0.5, time.Second, true)
	localCalled := false
	localTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		localCalled = true
		return jsonResponse(`{"model":"test-model","message":{"role":"assistant","content":"local answer"},"done":true}`), nil
	})
	local.client.Transport = localTransport
	local.streamClient.Transport = localTransport

	router := &Router{
		remoteClient:       remote,
		localClient:        local,
		config:             &config.LLMConfig{Router: config.RouterConfig{PreferRemote: true}},
		isOnline:           true,
		lastCheck:          time.Now(),
		remoteMaxRetries:   5,
		remoteRetryBackoff: time.Millisecond,
	}

	var deltas []StreamDelta
	_, err := router.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil,
		func(d StreamDelta) { deltas = append(deltas, d) })
	if err == nil {
		t.Fatal("expected an error after the mid-stream failure")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 — must not retry once a delta was already shown to the user", attempts)
	}
	if localCalled {
		t.Error("must not fall back to local once a delta was already shown to the user")
	}
	if len(deltas) != 1 || deltas[0].Text != "partial" {
		t.Errorf("deltas = %#v, want exactly the one partial delta before failure", deltas)
	}
}

func TestRetryableRemoteError(t *testing.T) {
	if !isRetryableRemoteError(&httpStatusError{statusCode: http.StatusTooManyRequests}) {
		t.Error("HTTP 429 should be retryable")
	}
	if !isRetryableRemoteError(&httpStatusError{statusCode: http.StatusBadGateway}) {
		t.Error("HTTP 502 should be retryable")
	}
	if isRetryableRemoteError(&httpStatusError{statusCode: http.StatusUnauthorized}) {
		t.Error("HTTP 401 should not be retryable")
	}
}
