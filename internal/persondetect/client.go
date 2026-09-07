// Package persondetect talks to the small local YOLOv8n HTTP wrapper
// (deploy/persondetect) — a dedicated, fast, CPU-only person detector
// used to poll every camera on a short interval. This is deliberately
// separate from internal/llm's vision support: DescribeImage is fine
// for an occasional "what's in this photo" ask, but at a few seconds
// (remote) to ~170s (local fallback) per call, it's far too slow and
// costly to run on every camera every few seconds — see docs/cameras.md.
package persondetect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one running deploy/persondetect instance.
type Client struct {
	baseURL string
	client  *http.Client
}

// Result is one image's detection outcome.
type Result struct {
	PersonDetected bool    `json:"person_detected"`
	Count          int     `json:"count"`
	Confidence     float64 `json:"confidence"`
}

// NewClient builds a Client for the person-detect service at baseURL
// (e.g. "http://localhost:8100"). timeout bounds one Detect call —
// inference measured well under 2s on this deployment's hardware, so a
// generous fixed timeout (unlike internal/llm's vision calls) is fine.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// Detect runs person detection on one JPEG frame.
func (c *Client) Detect(ctx context.Context, jpegBytes []byte) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/detect", bytes.NewReader(jpegBytes))
	if err != nil {
		return Result{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "image/jpeg")

	resp, err := c.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Result{}, fmt.Errorf("person-detect returned %d: %s", resp.StatusCode, string(body))
	}

	var result Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

// Healthy reports whether the service has finished loading its model
// and is ready to accept Detect calls — checked once before the first
// use of a freshly-started service rather than letting an early Detect
// call fail and retry (see cmd/smarthelper's camera security checker).
func (c *Client) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
