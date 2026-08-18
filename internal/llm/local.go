package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	APIFormatOllama = "ollama"
	APIFormatOpenAI = "openai"
)

// LocalClient implements Client for Ollama and local OpenAI-compatible servers.
type LocalClient struct {
	baseURL       string
	model         string
	client        *http.Client
	apiFormat     string
	apiKey        string
	temperature   float64
	supportsTools bool
}

// NewLocalClient creates a new Ollama client
func NewLocalClient(baseURL, model string, temperature float64, timeout time.Duration) *LocalClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3.1:8b"
	}
	return &LocalClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		model:         model,
		client:        &http.Client{Timeout: timeout},
		apiFormat:     APIFormatOllama,
		temperature:   temperature,
		supportsTools: true,
	}
}

// NewOpenAICompatibleLocalClient creates a local client for servers such as
// LM Studio that expose an OpenAI-compatible chat completions endpoint.
func NewOpenAICompatibleLocalClient(baseURL, model, apiKeyEnv string, temperature float64, timeout time.Duration) (*LocalClient, error) {
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	if model == "" {
		model = "default"
	}

	var apiKey string
	if apiKeyEnv != "" {
		apiKey = os.Getenv(apiKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("API key not found in env var %s", apiKeyEnv)
		}
	}

	return &LocalClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		model:         model,
		client:        &http.Client{Timeout: timeout},
		apiFormat:     APIFormatOpenAI,
		apiKey:        apiKey,
		temperature:   temperature,
		supportsTools: true,
	}, nil
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
	Messages []ollamaMessage `json:"messages"`
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
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func (c *LocalClient) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	if c.apiFormat == APIFormatOpenAI {
		client := &RemoteClient{
			baseURL:     c.baseURL,
			model:       c.model,
			apiKey:      c.apiKey,
			temperature: c.temperature,
			client:      c.client,
		}
		if !c.supportsTools && len(tools) > 0 {
			return chatWithPromptedTools(ctx, client, messages, tools)
		}
		response, err := client.Chat(ctx, messages, tools)
		if err != nil {
			return nil, err
		}
		stripLeakedReasoningMarker(response)
		parseLlamaToolCalls(response)
		return response, nil
	}

	ollamaMessages, err := toOllamaMessages(messages)
	if err != nil {
		return nil, err
	}

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
		Options:  map[string]any{"temperature": c.temperature},
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
			response.ToolCalls[i].Function.Arguments = string(tc.Function.Arguments)
		}
	}

	return response, nil
}

var (
	llamaToolCallPattern  = regexp.MustCompile(`(?s)<tool_call>\s*<function=([^>\n]+)>\s*(.*?)</function>\s*</tool_call>`)
	llamaParameterPattern = regexp.MustCompile(`(?s)<parameter=([^>\n]+)>\s*(.*?)\s*</parameter>`)

	// Observed once: llama-server with --skip-chat-parsing let a raw,
	// malformed reasoning-channel token fragment through verbatim (e.g.
	// "0thought\n<channel|>actual answer..."). Anchored to the very start
	// of the content so it can never eat a legitimate later occurrence.
	leakedReasoningMarkerPattern = regexp.MustCompile(`(?s)^\s*\S{0,32}\s*<channel\|>\s*`)
)

// stripLeakedReasoningMarker removes a leaked raw special-token fragment
// that can precede the real answer when llama-server runs with
// --skip-chat-parsing and the model/template's reasoning-channel format
// isn't fully re-rendered by the detokenizer. This is a compatibility
// workaround for what we've actually seen, not a general "clean up
// anything odd" filter.
func stripLeakedReasoningMarker(response *Response) {
	if response == nil {
		return
	}
	if loc := leakedReasoningMarkerPattern.FindStringIndex(response.Content); loc != nil {
		response.Content = strings.TrimSpace(response.Content[loc[1]:])
	}
}

// parseLlamaToolCalls converts the XML emitted by tool-aware GGUF chat
// templates when llama-server runs with --skip-chat-parsing into the canonical
// OpenAI-style representation used by the agent loop.
func parseLlamaToolCalls(response *Response) {
	if response == nil || len(response.ToolCalls) > 0 {
		return
	}

	matches := llamaToolCallPattern.FindAllStringSubmatch(response.Content, -1)
	if len(matches) == 0 {
		return
	}

	response.ToolCalls = make([]ToolCall, 0, len(matches))
	for index, match := range matches {
		arguments := make(map[string]any)
		for _, parameter := range llamaParameterPattern.FindAllStringSubmatch(match[2], -1) {
			arguments[strings.TrimSpace(parameter[1])] = strings.TrimSpace(parameter[2])
		}
		encodedArguments, err := json.Marshal(arguments)
		if err != nil {
			continue
		}

		call := ToolCall{ID: fmt.Sprintf("llama_call_%d", index), Type: "function"}
		call.Function.Name = strings.TrimSpace(match[1])
		call.Function.Arguments = string(encodedArguments)
		response.ToolCalls = append(response.ToolCalls, call)
	}
	response.Content = strings.TrimSpace(llamaToolCallPattern.ReplaceAllString(response.Content, ""))
}

const promptedToolsInstruction = `The server does not support native tool calling. ` +
	`When a tool is needed, reply with only this JSON object and no markdown: ` +
	`{"tool":"tool_name","arguments":{}}. Use only a tool from the definitions below. ` +
	`When a tool result is present, answer the user normally using that result. ` +
	`When no tool is needed, answer normally.`

type promptedToolCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func chatWithPromptedTools(
	ctx context.Context,
	client *RemoteClient,
	messages []Message,
	tools []ToolDefinition,
) (*Response, error) {
	// Models needing this fallback are the smallest/weakest ones, so the tool
	// contract is rendered in a compact one-line-per-tool form instead of full
	// JSON Schema — the schema boilerplate ("type":"object", "properties",
	// "additionalProperties") costs tokens without adding information a tiny
	// model uses. See docs/token-budget.md.
	systemContent := promptedToolsInstruction + "\nTools: " + compactToolDefinitions(tools)
	promptedMessages := make([]Message, 0, len(messages)+1)
	hasToolResult := false
	for _, message := range messages {
		switch {
		case len(message.ToolCalls) > 0:
			// The prompted protocol represents tool calls as plain assistant JSON;
			// the actual result below is all the model needs for the next turn.
			continue
		case message.Role == "system":
			systemContent += "\n" + message.Content
		case message.Role == "tool":
			hasToolResult = true
			promptedMessages = append(promptedMessages, Message{
				Role:    "user",
				Content: fmt.Sprintf("Tool result from %s: %s", message.Name, message.Content),
			})
		default:
			promptedMessages = append(promptedMessages, Message{
				Role:    message.Role,
				Content: message.Content,
				Name:    message.Name,
			})
		}
	}
	promptedMessages = append([]Message{{Role: "system", Content: systemContent}}, promptedMessages...)

	response, err := client.Chat(ctx, promptedMessages, nil)
	if err != nil {
		return nil, err
	}

	var call promptedToolCall
	candidate := strings.TrimSpace(response.Content)
	candidate = strings.TrimPrefix(candidate, "```json")
	candidate = strings.TrimPrefix(candidate, "```")
	candidate = strings.TrimSuffix(candidate, "```")
	candidate = strings.TrimSpace(candidate)
	if json.Unmarshal([]byte(candidate), &call) != nil && !hasToolResult {
		call = toolMention(candidate, tools)
	}
	if !hasToolResult && call.Tool != "" {
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		toolCall := ToolCall{ID: "prompted_call_0", Type: "function"}
		toolCall.Function.Name = call.Tool
		toolCall.Function.Arguments = string(call.Arguments)
		response.Content = ""
		response.ToolCalls = []ToolCall{toolCall}
	}

	return response, nil
}

// compactToolDefinitions renders tool definitions as one line per tool
// instead of full JSON Schema, e.g.:
//
//	memo(action:write|read|list|archive|delete, key?:string, content?:string): Write, read, ...
//
// This drops the JSON Schema envelope (type/properties/additionalProperties)
// that a tiny model doesn't need but still pays prompt-token cost for.
func compactToolDefinitions(tools []ToolDefinition) string {
	lines := make([]string, 0, len(tools))
	for _, tool := range tools {
		lines = append(lines, fmt.Sprintf("%s(%s): %s", tool.Name, compactParameters(tool.Parameters), tool.Description))
	}
	return strings.Join(lines, "\n")
}

func compactParameters(parameters any) string {
	schema, ok := parameters.(map[string]any)
	if !ok {
		return ""
	}
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return ""
	}
	requiredNames, _ := schema["required"].([]string)
	required := make(map[string]bool, len(requiredNames))
	for _, name := range requiredNames {
		required[name] = true
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		prop, _ := properties[name].(map[string]any)
		label, _ := prop["type"].(string)
		if enum, ok := prop["enum"].([]string); ok && len(enum) > 0 {
			label = strings.Join(enum, "|")
		}
		suffix := "?"
		if required[name] {
			suffix = ""
		}
		parts = append(parts, fmt.Sprintf("%s%s:%s", name, suffix, label))
	}
	return strings.Join(parts, ", ")
}

// toolMention is a compatibility fallback for very small models that correctly
// identify a tool but describe the call in prose instead of emitting JSON. It
// only accepts an unambiguous exact tool name from the supplied registry.
func toolMention(content string, tools []ToolDefinition) promptedToolCall {
	var matched string
	for _, tool := range tools {
		if !strings.Contains(content, tool.Name) {
			continue
		}
		if matched != "" {
			return promptedToolCall{}
		}
		matched = tool.Name
	}
	if matched == "" {
		return promptedToolCall{}
	}
	return promptedToolCall{Tool: matched, Arguments: json.RawMessage(`{}`)}
}

func toOllamaMessages(messages []Message) ([]ollamaMessage, error) {
	out := make([]ollamaMessage, 0, len(messages))
	for _, message := range messages {
		converted := ollamaMessage{
			Role:     message.Role,
			Content:  message.Content,
			ToolName: message.Name,
		}
		for _, call := range message.ToolCalls {
			arguments := json.RawMessage(call.Function.Arguments)
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			if !json.Valid(arguments) {
				return nil, fmt.Errorf("invalid arguments for %s: expected JSON", call.Function.Name)
			}
			toolCall := ollamaToolCall{}
			toolCall.Function.Name = call.Function.Name
			toolCall.Function.Arguments = arguments
			converted.ToolCalls = append(converted.ToolCalls, toolCall)
		}
		out = append(out, converted)
	}
	return out, nil
}
