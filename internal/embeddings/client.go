// Package embeddings calls an OpenAI-compatible /embeddings endpoint. Used
// by both the memo tool's semantic search and the document store — see
// docs/memo-search.md.
package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

// Client calls an OpenAI-compatible /embeddings endpoint.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewClient returns nil when cfg.BaseURL is empty — the feature is opt-in,
// since not every deployment has an embeddings-capable server.
func NewClient(cfg *config.EmbeddingsConfig) *Client {
	if cfg == nil {
		return nil
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil
	}
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Second
	}
	var apiKey string
	if cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   cfg.Model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: timeout},
	}
}

// Embed returns the embedding vector for text.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	payload, err := json.Marshal(map[string]any{"model": c.model, "input": text})
	if err != nil {
		return nil, fmt.Errorf("encode embeddings request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embeddings response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings request failed with status %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode embeddings response: %w", err)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings response had no vector")
	}
	return parsed.Data[0].Embedding, nil
}

// SetTransport overrides the underlying HTTP transport — test-only hook.
func (c *Client) SetTransport(rt http.RoundTripper) {
	c.client.Transport = rt
}

// CosineSimilarity returns a value in roughly [-1, 1]; 0 when the vectors
// are empty, mismatched in length, or either has zero magnitude.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
