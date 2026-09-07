package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// visionContentPart/visionMessage/visionRequest are a separate, narrower
// request shape from the ordinary []Message ([]string-Content) path used
// everywhere else in this package — only DescribeImage below ever needs
// the OpenAI vision "array of parts" content shape, so it's kept local to
// this one call rather than complicating Message for every other caller.
type visionContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

type visionMessage struct {
	Role    string              `json:"role"`
	Content []visionContentPart `json:"content"`
}

type visionRequest struct {
	Model       string          `json:"model"`
	Messages    []visionMessage `json:"messages"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

// visionMaxTokens leaves enough room for a reasoning model's own <think>
// block (see the trailing-think-tag stripping below) ahead of its actual
// answer — a small budget here just truncates mid-thought and returns no
// real description at all, which is exactly what happened during manual
// testing at max_tokens: 30. Even 700 wasn't always enough: a live test
// against a detailed, busy photo spent the entire budget mid-thought and
// never reached a closing </think> tag at all (stripThinkBlock then just
// returns the whole in-progress ramble, which the caller can still work
// with, but a real answer is strictly better).
const visionMaxTokens = 1800

// describeImageMaxAttempts/describeImageRetryStep are vars, not consts,
// so tests can shrink the backoff instead of a real test run waiting out
// several real seconds of retry delay. 4 attempts wasn't enough in live
// use — confirmed hitting the non-vision backend 4 times in a row (not
// implausible if the backend split is worse than 50/50, or the proxy's
// selection isn't independent per attempt) — bumped to 6.
var (
	describeImageMaxAttempts = 6
	describeImageRetryStep   = 500 * time.Millisecond
)

// DescribeImage sends a one-off vision request (a text prompt plus one
// image) to the remote OpenAI-compatible endpoint and returns the
// model's answer. Local has no vision model configured at all (see
// docs/vision.md), so this is remote-only with no local fallback —
// unlike Chat/ChatStream, a failure here isn't something local could
// ever have served instead.
//
// The proxy this deployment sits behind fans a single model name out
// across multiple upstream backends, and not all of them support image
// input — a request landing on a text-only one fails outright rather
// than degrading (confirmed live: "Multimodal data provided, but model
// does not support multimodal requests"). Retrying gives a real chance
// of landing on a different, vision-capable backend next time, so
// failures here are retried a few times before giving up.
func (c *RemoteClient) DescribeImage(ctx context.Context, prompt string, imageBytes []byte, mimeType string) (string, error) {
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(imageBytes)
	reqBody := visionRequest{
		Model: c.model,
		Messages: []visionMessage{{
			Role: "user",
			Content: []visionContentPart{
				{Type: "text", Text: prompt},
				{Type: "image_url", ImageURL: &visionImageURL{URL: dataURL}},
			},
		}},
		Temperature: c.getTemperature(),
		MaxTokens:   visionMaxTokens,
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < describeImageMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * describeImageRetryStep):
			}
		}

		content, err := c.describeImageOnce(ctx, jsonBody)
		if err == nil {
			return content, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("describe image: %w", lastErr)
}

func (c *RemoteClient) describeImageOnce(ctx context.Context, jsonBody []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.organization != "" {
		req.Header.Set("OpenAI-Organization", c.organization)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &httpStatusError{provider: "openai", statusCode: resp.StatusCode, body: string(body)}
	}

	var openAIResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	content := stripThinkBlock(openAIResp.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("empty response")
	}
	return content, nil
}

// stripThinkBlock drops a leading reasoning-model <think>...</think>
// block, keeping only what follows it — the caller wants the answer,
// not the model's scratch work. A response with no closing tag (an
// answer that never used one, or one truncated before finishing its
// thought) is returned as-is rather than discarded.
func stripThinkBlock(content string) string {
	if idx := strings.LastIndex(content, "</think>"); idx != -1 {
		return strings.TrimSpace(content[idx+len("</think>"):])
	}
	return strings.TrimSpace(content)
}
