package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// DescribeImage sends a one-off vision request to a local OpenAI-
// compatible server (see NewOpenAICompatibleLocalClient) — only
// supported in that mode, since that's the only local setup this
// deployment actually runs (llama-server); the native Ollama API has a
// different image-embedding request shape this doesn't implement.
//
// Unlike RemoteClient.DescribeImage, there's no retry loop: a local
// server is either up or it isn't, not fanned out across flaky upstream
// backends. It is, however, slow — a real photo measured ~170s
// end-to-end on this deployment's modest CPU-only hardware — so this
// uses the same unbounded-Timeout http.Client as ChatStream (relying on
// the caller's context, not a fixed request timeout, to bound it) rather
// than c.client, whose configured timeout is tuned for ordinary text
// chat and would cut this off partway through.
func (c *LocalClient) DescribeImage(ctx context.Context, prompt string, imageBytes []byte, mimeType string) (string, error) {
	if c.apiFormat != APIFormatOpenAI {
		return "", fmt.Errorf("image description requires api_format: openai")
	}

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

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &httpStatusError{provider: "local", statusCode: resp.StatusCode, body: string(body)}
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
