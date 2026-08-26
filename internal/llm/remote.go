package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// RemoteClient implements Client for OpenAI-compatible APIs
type RemoteClient struct {
	baseURL      string
	model        string
	apiKey       string
	organization string
	// temperature is guarded by temperatureMu so it can be changed live
	// (see SetTemperature) from the web UI's settings page while requests
	// are in flight, without a restart.
	temperature   float64
	temperatureMu sync.RWMutex
	client        *http.Client
	// streamClient has no Client.Timeout: that field bounds the entire
	// request including reading the response body, so it would truncate a
	// legitimately slow-but-progressing stream mid-answer. The caller's
	// context (e.g. the web server's per-request timeout, chosen with
	// multi-step tool-using turns in mind) is the correct authority for
	// how long a streaming exchange may run instead.
	streamClient *http.Client
}

type httpStatusError struct {
	provider   string
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s error %d: %s", e.provider, e.statusCode, e.body)
}

// NewRemoteClient creates a new OpenAI-compatible client
func NewRemoteClient(baseURL, model, apiKeyEnv, organization string, temperature float64, timeout time.Duration) (*RemoteClient, error) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}

	apiKey := os.Getenv(apiKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("API key not found in env var %s", apiKeyEnv)
	}

	return &RemoteClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		model:        model,
		apiKey:       apiKey,
		organization: organization,
		temperature:  temperature,
		client:       &http.Client{Timeout: timeout},
		streamClient: &http.Client{},
	}, nil
}

func (c *RemoteClient) Model() string {
	return c.model
}

func (c *RemoteClient) Provider() string {
	return "remote"
}

// SetTemperature changes the sampling temperature used by future requests
// (see docs/settings.md) — safe to call while other requests are in
// flight; they keep whatever value they already read.
func (c *RemoteClient) SetTemperature(v float64) {
	c.temperatureMu.Lock()
	c.temperature = v
	c.temperatureMu.Unlock()
}

func (c *RemoteClient) getTemperature() float64 {
	c.temperatureMu.RLock()
	defer c.temperatureMu.RUnlock()
	return c.temperature
}

// OpenAI request/response types
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	Tools       []openAIToolDef `json:"tools,omitempty"`
	ToolChoice  string          `json:"tool_choice,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	// StreamOptions is only meaningful with Stream true — without it, an
	// OpenAI-compatible streaming response never carries a usage object
	// at all (see openAIStreamChunk.Usage in sse.go), leaving token
	// counts silently zero for the whole streaming path.
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIToolDef struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type openAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   Usage          `json:"usage"`
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func (c *RemoteClient) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	// Convert tools
	var openAITools []openAIToolDef
	for _, t := range tools {
		openAITools = append(openAITools, openAIToolDef{
			Type:     "function",
			Function: t,
		})
	}

	reqBody := openAIRequest{
		Model:       c.model,
		Messages:    messages,
		Tools:       openAITools,
		Temperature: c.getTemperature(),
	}

	if len(tools) > 0 {
		reqBody.ToolChoice = "auto"
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
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
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, &httpStatusError{
			provider:   "openai",
			statusCode: resp.StatusCode,
			body:       string(body),
		}
	}

	var openAIResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := openAIResp.Choices[0]
	response := &Response{
		Content:   choice.Message.Content,
		ToolCalls: choice.Message.ToolCalls,
		Model:     openAIResp.Model,
		Usage:     openAIResp.Usage,
	}

	return response, nil
}

// ChatStream streams the answer via OpenAI-compatible SSE. Tool calls
// always arrive as a structured field in this wire format, never mixed
// into content, so no fold detection is needed here — see the
// OpenAI-compatible LocalClient for the case that does need it.
func (c *RemoteClient) ChatStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error) {
	var openAITools []openAIToolDef
	for _, t := range tools {
		openAITools = append(openAITools, openAIToolDef{
			Type:     "function",
			Function: t,
		})
	}

	reqBody := openAIRequest{
		Model:         c.model,
		Messages:      messages,
		Tools:         openAITools,
		Temperature:   c.getTemperature(),
		Stream:        true,
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
	}
	if len(tools) > 0 {
		reqBody.ToolChoice = "auto"
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.organization != "" {
		req.Header.Set("OpenAI-Organization", c.organization)
	}

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, &httpStatusError{
			provider:   "openai",
			statusCode: resp.StatusCode,
			body:       string(body),
		}
	}

	return parseOpenAISSEStream(resp.Body, onDelta, false)
}
