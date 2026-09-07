package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteClientDescribeImageSendsVisionContent(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_VISION_KEY"
	t.Setenv(keyEnv, "vision-secret")

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vision-secret" {
			t.Errorf("Authorization = %q", got)
		}

		var request visionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(request.Messages) != 1 || len(request.Messages[0].Content) != 2 {
			t.Fatalf("unexpected request shape: %+v", request)
		}
		textPart, imagePart := request.Messages[0].Content[0], request.Messages[0].Content[1]
		if textPart.Type != "text" || textPart.Text != "what is this?" {
			t.Errorf("text part = %+v", textPart)
		}
		if imagePart.Type != "image_url" || imagePart.ImageURL == nil ||
			!strings.HasPrefix(imagePart.ImageURL.URL, "data:image/png;base64,") {
			t.Errorf("image part = %+v", imagePart)
		}

		return jsonResponse(`{
			"model":"text",
			"choices":[{"index":0,"message":{"role":"assistant","content":"a red square"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`), nil
	})

	client, err := NewRemoteClient("https://remote.test/v1/", "text", keyEnv, "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	got, err := client.DescribeImage(context.Background(), "what is this?", []byte("fake-png-bytes"), "image/png")
	if err != nil {
		t.Fatalf("DescribeImage returned error: %v", err)
	}
	if got != "a red square" {
		t.Errorf("got %q, want %q", got, "a red square")
	}
}

func TestRemoteClientDescribeImageStripsThinkBlock(t *testing.T) {
	const keyEnv = "SMARTHELPER_TEST_VISION_THINK_KEY"
	t.Setenv(keyEnv, "vision-secret")

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{
			"model":"text",
			"choices":[{"index":0,"message":{"role":"assistant","content":"\n<think>\nreasoning about pixels\n</think>\nA black cow.\n"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`), nil
	})

	client, err := NewRemoteClient("https://remote.test/v1/", "text", keyEnv, "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	got, err := client.DescribeImage(context.Background(), "what is this?", []byte("fake"), "image/png")
	if err != nil {
		t.Fatalf("DescribeImage returned error: %v", err)
	}
	if got != "A black cow." {
		t.Errorf("got %q, want %q", got, "A black cow.")
	}
}

func TestRemoteClientDescribeImageRetriesOnFailure(t *testing.T) {
	origAttempts, origStep := describeImageMaxAttempts, describeImageRetryStep
	describeImageMaxAttempts, describeImageRetryStep = 3, time.Millisecond
	t.Cleanup(func() { describeImageMaxAttempts, describeImageRetryStep = origAttempts, origStep })

	const keyEnv = "SMARTHELPER_TEST_VISION_RETRY_KEY"
	t.Setenv(keyEnv, "vision-secret")

	var calls int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       jsonResponse(`{"error":{"message":"Multimodal data provided, but model does not support multimodal requests."}}`).Body,
			}, nil
		}
		return jsonResponse(`{
			"model":"text",
			"choices":[{"index":0,"message":{"role":"assistant","content":"a cow"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`), nil
	})

	client, err := NewRemoteClient("https://remote.test/v1/", "text", keyEnv, "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	got, err := client.DescribeImage(context.Background(), "what is this?", []byte("fake"), "image/png")
	if err != nil {
		t.Fatalf("DescribeImage returned error: %v", err)
	}
	if got != "a cow" {
		t.Errorf("got %q, want %q", got, "a cow")
	}
	if calls != 2 {
		t.Errorf("expected 2 attempts (fail then succeed), got %d", calls)
	}
}

func TestRemoteClientDescribeImageFailsAfterMaxAttempts(t *testing.T) {
	origAttempts, origStep := describeImageMaxAttempts, describeImageRetryStep
	describeImageMaxAttempts, describeImageRetryStep = 2, time.Millisecond
	t.Cleanup(func() { describeImageMaxAttempts, describeImageRetryStep = origAttempts, origStep })

	const keyEnv = "SMARTHELPER_TEST_VISION_FAIL_KEY"
	t.Setenv(keyEnv, "vision-secret")

	var calls int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       jsonResponse(`{"error":{"message":"nope"}}`).Body,
		}, nil
	})

	client, err := NewRemoteClient("https://remote.test/v1/", "text", keyEnv, "", 0.8, time.Second)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.client.Transport = transport

	_, err = client.DescribeImage(context.Background(), "what is this?", []byte("fake"), "image/png")
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if calls != 2 {
		t.Errorf("expected 2 attempts, got %d", calls)
	}
}
