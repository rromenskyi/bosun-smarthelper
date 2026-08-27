package llm

import (
	"context"
)

// Message represents a chat message
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // set on assistant messages that call tools
	ToolCallID string     `json:"tool_call_id,omitempty"` // set on tool result messages
}

// ToolCall represents a function/tool call from the LLM
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // function
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Response represents an LLM response
type Response struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Model     string     `json:"model"`
	Usage     Usage      `json:"usage"`
	// BackendModel, when non-empty, is a more specific identity than
	// Model — a reverse proxy in front of the provider can report the
	// real backend behind a generic model alias via an X-Backend-Model
	// response header (RemoteClient reads it), since Model itself just
	// echoes back whatever alias the request asked for.
	BackendModel string `json:"-"`
}

// Usage tracks token usage
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Client defines the interface for LLM providers
type Client interface {
	Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error)
	Model() string
	Provider() string // "local" or "remote"
}

// StreamDelta is one incremental piece of a streamed response. Kind
// distinguishes ordinary answer text ("prose") from text that is (or may
// be part of) a tool-call encoding a client shouldn't display directly
// ("fold") — see the OpenAI-compatible local client, which has to detect
// this from raw content since llama.cpp's --skip-chat-parsing mode emits
// tool calls as XML mixed into normal text rather than a structured field.
type StreamDelta struct {
	Kind string // "prose" or "fold"
	Text string
}

// StreamingClient is an optional capability: a Client may additionally
// support streaming, checked via a type assertion (same pattern as
// NetworkDependentTool in internal/tools). It still returns the complete
// assembled Response at the end, so callers that only care about the final
// result don't need special-casing.
type StreamingClient interface {
	ChatStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error)
}

// ToolDefinition describes a tool/function for the LLM
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema
}

// ProviderType indicates which provider to use
type ProviderType string

const (
	ProviderLocal  ProviderType = "local"
	ProviderRemote ProviderType = "remote"
)
