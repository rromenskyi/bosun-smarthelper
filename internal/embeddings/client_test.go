package embeddings

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewClientNilWhenBaseURLEmpty(t *testing.T) {
	if NewClient(&config.EmbeddingsConfig{}) != nil {
		t.Error("expected a nil client when base_url is empty")
	}
	if NewClient(nil) != nil {
		t.Error("expected a nil client for a nil config")
	}
}

func TestClientEmbed(t *testing.T) {
	var gotBody map[string]any
	client := NewClient(&config.EmbeddingsConfig{BaseURL: "http://embed.test/v1", Model: "embed"})
	client.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want /v1/embeddings", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`)),
		}, nil
	}))

	vector, err := client.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if gotBody["model"] != "embed" || gotBody["input"] != "hello" {
		t.Errorf("request body = %#v", gotBody)
	}
	if len(vector) != 3 || vector[0] != 0.1 {
		t.Errorf("vector = %v", vector)
	}
}

func TestClientEmbedErrorStatus(t *testing.T) {
	client := NewClient(&config.EmbeddingsConfig{BaseURL: "http://embed.test/v1"})
	client.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"boom"}`)),
		}, nil
	}))
	if _, err := client.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestCosineSimilarity(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0, 0}, []float32{1, 0, 0}); got != 1 {
		t.Errorf("identical vectors similarity = %v, want 1", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal vectors similarity = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("mismatched-length similarity = %v, want 0", got)
	}
	if got := CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("empty vectors similarity = %v, want 0", got)
	}
}
