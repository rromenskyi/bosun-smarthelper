package llm

import (
	"bufio"
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
	"sync"
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
	supportsTools bool
	streamEnabled bool
	// temperature is guarded by temperatureMu so it can be changed live
	// (see SetTemperature) from the web UI's settings page while requests
	// are in flight, without a restart.
	temperature   float64
	temperatureMu sync.RWMutex
	// streamClient has no Client.Timeout — see the identical field on
	// RemoteClient for why a streaming request must not be bounded by a
	// fixed total-duration timeout.
	streamClient *http.Client
}

// NewLocalClient creates a new Ollama client
func NewLocalClient(baseURL, model string, temperature float64, timeout time.Duration, streamEnabled bool) *LocalClient {
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
		streamEnabled: streamEnabled,
		streamClient:  &http.Client{},
	}
}

// NewOpenAICompatibleLocalClient creates a local client for servers such as
// LM Studio that expose an OpenAI-compatible chat completions endpoint.
func NewOpenAICompatibleLocalClient(baseURL, model, apiKeyEnv string, temperature float64, timeout time.Duration, streamEnabled bool) (*LocalClient, error) {
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
		streamEnabled: streamEnabled,
		streamClient:  &http.Client{},
	}, nil
}

func (c *LocalClient) Model() string {
	return c.model
}

func (c *LocalClient) Provider() string {
	return "local"
}

// SetTemperature changes the sampling temperature used by future requests
// (see docs/settings.md) — safe to call while other requests are in
// flight; they keep whatever value they already read.
func (c *LocalClient) SetTemperature(v float64) {
	c.temperatureMu.Lock()
	c.temperature = v
	c.temperatureMu.Unlock()
}

func (c *LocalClient) getTemperature() float64 {
	c.temperatureMu.RLock()
	defer c.temperatureMu.RUnlock()
	return c.temperature
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
			baseURL:      c.baseURL,
			model:        c.model,
			apiKey:       c.apiKey,
			temperature:  c.getTemperature(),
			client:       c.client,
			streamClient: c.streamClient,
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
		Options:  map[string]any{"temperature": c.getTemperature()},
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

// ChatStream streams the answer. The prompted-JSON tool fallback
// (supports_tools: false) is intentionally excluded — it needs the
// complete response to recognize its tool-call JSON blob, so it falls back
// to the non-streaming path via chatWithPromptedTools.
func (c *LocalClient) ChatStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error) {
	if !c.streamEnabled {
		resp, err := c.Chat(ctx, messages, tools)
		if err != nil {
			return nil, err
		}
		if resp.Content != "" {
			onDelta(StreamDelta{Kind: "prose", Text: resp.Content})
		}
		return resp, nil
	}
	if c.apiFormat == APIFormatOpenAI {
		if !c.supportsTools && len(tools) > 0 {
			client := &RemoteClient{
				baseURL:      c.baseURL,
				model:        c.model,
				apiKey:       c.apiKey,
				temperature:  c.getTemperature(),
				client:       c.client,
				streamClient: c.streamClient,
			}
			return chatWithPromptedTools(ctx, client, messages, tools)
		}
		return c.chatStreamOpenAI(ctx, messages, tools, onDelta)
	}
	return c.chatStreamOllama(ctx, messages, tools, onDelta)
}

func (c *LocalClient) chatStreamOpenAI(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error) {
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
		Stream:      true,
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

	// detectFold: true — this is the raw --skip-chat-parsing passthrough
	// path, so content may contain a leaked reasoning-marker prefix and/or
	// an embedded <tool_call> block that must never reach the user as-is.
	response, err := parseOpenAISSEStream(resp.Body, onDelta, true)
	if err != nil {
		return nil, err
	}
	stripLeakedReasoningMarker(response)
	parseLlamaToolCalls(response)
	return response, nil
}

func (c *LocalClient) chatStreamOllama(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error) {
	ollamaMessages, err := toOllamaMessages(messages)
	if err != nil {
		return nil, err
	}

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
		Stream:   true,
		Options:  map[string]any{"temperature": c.getTemperature()},
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

	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var content strings.Builder
	var model string
	var promptTokens, completionTokens int
	var toolCalls []ToolCall

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk ollamaResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue // skip a malformed line rather than fail the whole stream
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Message.Content != "" {
			content.WriteString(chunk.Message.Content)
			if onDelta != nil {
				// Ollama's own tool_calls field is separate and structured
				// (never mixed into content), so no fold detection needed.
				onDelta(StreamDelta{Kind: "prose", Text: chunk.Message.Content})
			}
		}
		if len(chunk.Message.ToolCalls) > 0 {
			toolCalls = make([]ToolCall, len(chunk.Message.ToolCalls))
			for i, tc := range chunk.Message.ToolCalls {
				toolCalls[i] = ToolCall{ID: fmt.Sprintf("call_%d", i), Type: "function"}
				toolCalls[i].Function.Name = tc.Function.Name
				toolCalls[i].Function.Arguments = string(tc.Function.Arguments)
			}
		}
		if chunk.Done {
			promptTokens = chunk.PromptEvalCount
			completionTokens = chunk.EvalCount
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	return &Response{
		Content:   content.String(),
		ToolCalls: toolCalls,
		Model:     model,
		Usage: Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}, nil
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

// parsePromptedToolCall decodes a prompted tool-call JSON object into a
// promptedToolCall. The documented shape nests parameters under "arguments"
// ({"tool":"name","arguments":{"query":"..."}}), but a weak model will
// frequently flatten them onto the top-level object instead
// ({"tool":"name","query":"..."}) — tolerate both, since insisting on the
// nested shape silently drops every argument the model actually gave.
func parsePromptedToolCall(raw []byte) (promptedToolCall, error) {
	var call promptedToolCall
	if err := json.Unmarshal(raw, &call); err != nil {
		return call, err
	}
	// A weak model echoes the compact tool signature verbatim
	// ("get_gps(): Get current GPS location.", from compactToolDefinitions)
	// instead of just the bare name — especially for zero-argument tools,
	// where the whole "name()" reads as one token. Strip everything from
	// the first "(" onward so "get_gps()" still matches the registered
	// tool "get_gps" instead of failing as an unknown tool.
	if idx := strings.IndexByte(call.Tool, '('); idx >= 0 {
		call.Tool = strings.TrimSpace(call.Tool[:idx])
	}
	if len(call.Arguments) > 0 {
		return call, nil
	}
	var flattened map[string]json.RawMessage
	if err := json.Unmarshal(raw, &flattened); err != nil {
		return call, nil
	}
	delete(flattened, "tool")
	delete(flattened, "arguments")
	if len(flattened) == 0 {
		return call, nil
	}
	if args, err := json.Marshal(flattened); err == nil {
		call.Arguments = args
	}
	return call, nil
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

	candidate := strings.TrimSpace(response.Content)
	candidate = strings.TrimPrefix(candidate, "```json")
	candidate = strings.TrimPrefix(candidate, "```")
	candidate = strings.TrimSuffix(candidate, "```")
	candidate = strings.TrimSpace(candidate)
	call, parseErr := parsePromptedToolCall([]byte(candidate))
	if parseErr != nil && !hasToolResult {
		// A weak model rarely emits *pure* JSON and nothing else — narration
		// before/after the object ("Sure, I'll check: {...} let me know")
		// makes the strict parse above fail even though a perfectly valid
		// tool call is embedded in there. Try to pull just the JSON object
		// out before giving up on structured parsing entirely and falling
		// back to toolMention's name-only match (which always discards
		// arguments — see its own doc comment).
		if object, ok := extractJSONObject(candidate); ok {
			call, parseErr = parsePromptedToolCall([]byte(object))
		}
		if parseErr != nil {
			call = toolMention(candidate, tools)
		}
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

// extractJSONObject finds the first balanced top-level `{...}` object in s,
// tolerating surrounding prose, by tracking brace depth and skipping over
// braces that appear inside a quoted string (so a query like
// `{"query": "a {weird} value"}` doesn't confuse the scan). Returns false if
// no closing brace ever balances the first opening one.
func extractJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
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
