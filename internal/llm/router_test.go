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
		localClient: NewLocalClient("", "", time.Second),
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
