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
