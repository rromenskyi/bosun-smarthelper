package llm

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

func TestRouterActiveProvider(t *testing.T) {
	router := &Router{
		localClient: NewLocalClient("", "", 0.5, time.Second, true),
		config:      &config.LLMConfig{},
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

func TestRouterSetTemperatures(t *testing.T) {
	t.Setenv("SMARTHELPER_TEST_ROUTER_TEMP_KEY", "secret")
	local := NewLocalClient("", "", 0.5, time.Second, true)
	remote, err := NewRemoteClient("https://remote.test/v1", "text", "SMARTHELPER_TEST_ROUTER_TEMP_KEY", "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create remote client: %v", err)
	}

	router := &Router{localClient: local, remoteClient: remote, config: &config.LLMConfig{}}
	router.SetTemperatures(0.3, 0.1)

	if got := remote.getTemperature(); got != 0.3 {
		t.Errorf("remote temperature = %v, want 0.3", got)
	}
	if got := local.getTemperature(); got != 0.1 {
		t.Errorf("local temperature = %v, want 0.1", got)
	}
}

func TestRouterProviderOverride(t *testing.T) {
	t.Setenv("SMARTHELPER_TEST_ROUTER_OVERRIDE_KEY", "secret")
	remote, err := NewRemoteClient("https://remote.test/v1", "text", "SMARTHELPER_TEST_ROUTER_OVERRIDE_KEY", "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create remote client: %v", err)
	}
	router := &Router{
		localClient:  NewLocalClient("", "", 0.5, time.Second, true),
		remoteClient: remote,
		config:       &config.LLMConfig{Router: config.RouterConfig{PreferRemote: true}},
		isOnline:     true,
	}

	if got := router.ProviderOverride(); got != "auto" {
		t.Errorf("default override = %q, want auto", got)
	}
	if got := router.CurrentProvider(); got != "remote" {
		t.Errorf("auto + online + prefer_remote = %q, want remote", got)
	}

	if err := router.SetProviderOverride("local"); err != nil {
		t.Fatalf("SetProviderOverride(local): %v", err)
	}
	if got := router.ProviderOverride(); got != "local" {
		t.Errorf("override = %q, want local", got)
	}
	if got := router.CurrentProvider(); got != "local" {
		t.Errorf("forced local while online = %q, want local", got)
	}
	client, err := router.GetClient(context.Background())
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if client.Provider() != "local" {
		t.Errorf("GetClient provider = %q, want local", client.Provider())
	}

	if err := router.SetProviderOverride("remote"); err != nil {
		t.Fatalf("SetProviderOverride(remote): %v", err)
	}
	if got := router.CurrentProvider(); got != "remote" {
		t.Errorf("forced remote = %q, want remote", got)
	}

	if err := router.SetProviderOverride("auto"); err != nil {
		t.Fatalf("SetProviderOverride(auto): %v", err)
	}
	if got := router.ProviderOverride(); got != "auto" {
		t.Errorf("override after reset = %q, want auto", got)
	}

	if err := router.SetProviderOverride("sideways"); err == nil {
		t.Error("expected an error for an invalid override value")
	}
}

func TestRouterProviderOverrideRemoteFallsBackWhenUnconfigured(t *testing.T) {
	router := &Router{
		localClient: NewLocalClient("", "", 0.5, time.Second, true),
		config:      &config.LLMConfig{},
	}
	if err := router.SetProviderOverride("remote"); err != nil {
		t.Fatalf("SetProviderOverride(remote): %v", err)
	}
	// No remote client configured at all — the override can't be honored,
	// so this should fall back to automatic selection (local) instead of
	// erroring.
	client, err := router.GetClient(context.Background())
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if client.Provider() != "local" {
		t.Errorf("GetClient provider = %q, want local", client.Provider())
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

func checkTargetRouter(t *testing.T, target string) *Router {
	t.Helper()
	return &Router{config: &config.LLMConfig{Router: config.RouterConfig{
		CheckTarget:  target,
		CheckTimeout: "1s",
	}}}
}

// TestRouterCheckConnectivityRetriesBeforeSucceeding is a regression test
// for a real report: a single failed connectivity check against a
// remote provider known to be occasionally slow flipped the whole router
// to "offline" — and everything to the local model — for a full
// check_interval, even though the outage was momentary.
func TestRouterCheckConnectivityRetriesBeforeSucceeding(t *testing.T) {
	original := connectivityCheckRetryDelay
	connectivityCheckRetryDelay = time.Millisecond
	defer func() { connectivityCheckRetryDelay = original }()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < connectivityCheckAttempts {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	router := checkTargetRouter(t, server.URL)
	if !router.CheckConnectivity(context.Background()) {
		t.Error("want online once an attempt within the retry budget succeeds")
	}
	if got := calls.Load(); got != connectivityCheckAttempts {
		t.Errorf("calls = %d, want exactly %d (stop retrying once it succeeds)", got, connectivityCheckAttempts)
	}
}

func TestRouterCheckConnectivityFailsAfterExhaustingRetries(t *testing.T) {
	original := connectivityCheckRetryDelay
	connectivityCheckRetryDelay = time.Millisecond
	defer func() { connectivityCheckRetryDelay = original }()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	router := checkTargetRouter(t, server.URL)
	if router.CheckConnectivity(context.Background()) {
		t.Error("want offline once every attempt in the retry budget fails")
	}
	if got := calls.Load(); got != connectivityCheckAttempts {
		t.Errorf("calls = %d, want %d", got, connectivityCheckAttempts)
	}
	if router.IsOnline() {
		t.Error("IsOnline() must reflect the failed check")
	}
}

func TestRouterCheckConnectivitySucceedsOnFirstTryWithoutDelay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	router := checkTargetRouter(t, server.URL)
	start := time.Now()
	online := router.CheckConnectivity(context.Background())
	elapsed := time.Since(start)

	if !online {
		t.Error("want online")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 — a first-try success must not retry", calls.Load())
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want fast — no retry delay when the first attempt already succeeded", elapsed)
	}
}

func TestRouterCheckConnectivityLogsFailureAfterRetries(t *testing.T) {
	original := connectivityCheckRetryDelay
	connectivityCheckRetryDelay = time.Millisecond
	defer func() { connectivityCheckRetryDelay = original }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var logBuf bytes.Buffer
	router := checkTargetRouter(t, server.URL)
	router.SetLogger(slog.New(slog.NewTextHandler(&logBuf, nil)))

	router.CheckConnectivity(context.Background())

	if !strings.Contains(logBuf.String(), "connectivity check failed") {
		t.Errorf("log output = %q, want a failure warning naming the connectivity check", logBuf.String())
	}
}

func TestRouterCheckConnectivityWithoutLoggerDoesNotPanic(t *testing.T) {
	original := connectivityCheckRetryDelay
	connectivityCheckRetryDelay = time.Millisecond
	defer func() { connectivityCheckRetryDelay = original }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	// No SetLogger call — matches every Router built as a bare struct
	// literal elsewhere in this file.
	router := checkTargetRouter(t, server.URL)
	if router.CheckConnectivity(context.Background()) {
		t.Error("want offline")
	}
}
