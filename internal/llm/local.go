package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LocalClient implements Client for Ollama
type LocalClient struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewLocalClient creates a new Ollama client
func NewLocalClient(baseURL, model string, timeout time.Duration) *LocalClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3.1:8b"
	}
	return &LocalClient{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *LocalClient) Model() string {
	return c.model
}

func (c *LocalClient) Provider() string {
	return "local"
}

// Ollama request/response types
type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Tools    []ollamaToolDef `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaToolDef struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type ollamaResponse struct {
	Model           string        `json:"model"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	EvalCount       int           `json:"eval_count"`
	PromptEvalCount int           `json:"prompt_eval_count"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (c *LocalClient) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	// Convert messages
	ollamaMessages := make([]Message, len(messages))
	copy(ollamaMessages, messages)

	// Convert tools
	var ollamaTools []ollamaToolDef
	for _, t := range tools {
		ollamaTools = append(ollamaTools, ollamaToolDef{
			Type:     "function",
			Function: t,
		})
	}

	reqBody := ollamaRequest{
		Model:    c.model,
		Messages: ollamaMessages,
		Tools:    ollamaTools,
		Stream:   false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/chat", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(body))
	}

	var ollamaResp ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Convert response
	response := &Response{
		Content: ollamaResp.Message.Content,
		Model:   ollamaResp.Model,
		Usage: Usage{
			PromptTokens:     ollamaResp.PromptEvalCount,
			CompletionTokens: ollamaResp.EvalCount,
			TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		},
	}

	if len(ollamaResp.Message.ToolCalls) > 0 {
		response.ToolCalls = make([]ToolCall, len(ollamaResp.Message.ToolCalls))
		for i, tc := range ollamaResp.Message.ToolCalls {
			response.ToolCalls[i] = ToolCall{
				ID:   fmt.Sprintf("call_%d", i),
				Type: "function",
			}
			response.ToolCalls[i].Function.Name = tc.Function.Name
			response.ToolCalls[i].Function.Arguments = tc.Function.Arguments
		}
	}

	return response, nil
}
