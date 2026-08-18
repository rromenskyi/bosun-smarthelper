// Package agent wires an LLM client to a tool registry: it runs the
// conversation loop that calls the model, executes any tool calls it
// requests, feeds the results back, and repeats until a final answer.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/roman220/ai-local-smarthelper/internal/errlog"
	"github.com/roman220/ai-local-smarthelper/internal/llm"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

const systemPrompt = `Be concise. Use available tools for live data, sensors, and memos. ` +
	`Never delete a memo unless explicitly asked; archive old ones instead. ` +
	`For mountain weather use a named mountain, park, or pass, never a nearby city; clarify ambiguity. ` +
	`Don't narrate or acknowledge your own instructions (language, style, persona); just answer directly.`

// maxToolIterations bounds the tool-call loop so a misbehaving model can't
// spin forever.
const maxToolIterations = 5

// ChatClient is the subset of llm.Client (or llm.Router) the agent needs.
type ChatClient interface {
	Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition) (*llm.Response, error)
}

// HistoryMessage is a completed user or assistant message retained between
// turns. Internal tool protocol messages are intentionally not persisted.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Agent runs the LLM ⇄ tools conversation loop for a single request.
type Agent struct {
	client              ChatClient
	registry            *tools.Registry
	networkAvailability func(context.Context) bool
	nameRU              string
	nameEN              string
	stylePrompt         string
	errLog              *errlog.Logger
}

// New creates an Agent backed by the given chat client and tool registry.
func New(
	client ChatClient,
	registry *tools.Registry,
	networkAvailability ...func(context.Context) bool,
) *Agent {
	agent := &Agent{client: client, registry: registry, nameRU: "Старпом", nameEN: "Bosun"}
	if len(networkAvailability) > 0 {
		agent.networkAvailability = networkAvailability[0]
	}
	return agent
}

// SetPersona configures the user-facing assistant identity and optional style.
func (a *Agent) SetPersona(nameRU, nameEN, stylePrompt string) {
	if strings.TrimSpace(nameRU) != "" {
		a.nameRU = strings.TrimSpace(nameRU)
	}
	if strings.TrimSpace(nameEN) != "" {
		a.nameEN = strings.TrimSpace(nameEN)
	}
	a.stylePrompt = strings.TrimSpace(stylePrompt)
}

// SetErrorLog wires a failure log for tool and LLM-call errors. A nil logger
// (the default) means failures are simply returned to the caller as before,
// not recorded anywhere durable.
func (a *Agent) SetErrorLog(logger *errlog.Logger) {
	a.errLog = logger
}

// Ask sends a single user message through the conversation loop, executing
// any tool calls the model requests, and returns its final text answer.
func (a *Agent) Ask(ctx context.Context, userMessage string) (string, error) {
	return a.AskWithHistory(ctx, userMessage, nil, "")
}

// AskWithHistory sends a user message with completed prior conversation
// turns. language is a BCP-47-ish hint ("ru", "en", or "" to let the model
// infer it from the message) — it's folded into the system prompt rather
// than the user turn so the model treats it as standing context, not a
// fresh command to acknowledge on every message.
func (a *Agent) AskWithHistory(
	ctx context.Context,
	userMessage string,
	history []HistoryMessage,
	language string,
) (string, error) {
	online := a.isOnline(ctx)
	toolDefs := a.toolDefinitions(online)
	prompt := fmt.Sprintf("You are %s (%s). ", a.nameEN, a.nameRU) + systemPrompt
	switch language {
	case "ru":
		prompt += " Respond in Russian."
	case "en":
		prompt += " Respond in English."
	}
	if a.stylePrompt != "" {
		prompt += " Response style: " + a.stylePrompt
	}
	if !online {
		prompt += ` Offline: do not claim live internet access.`
	}
	messages := make([]llm.Message, 0, len(history)+2)
	messages = append(messages, llm.Message{Role: "system", Content: prompt})
	for _, message := range history {
		if (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Content) == "" {
			continue
		}
		messages = append(messages, llm.Message{Role: message.Role, Content: message.Content})
	}
	messages = append(messages, llm.Message{Role: "user", Content: userMessage})

	for i := 0; i < maxToolIterations; i++ {
		resp, err := a.client.Chat(ctx, messages, toolDefs)
		if err != nil {
			a.errLog.Record("llm_chat", a.chatProvider(), err)
			return "", fmt.Errorf("chat: %w", err)
		}

		if len(resp.ToolCalls) == 0 {
			if strings.TrimSpace(resp.Content) == "" {
				return "", fmt.Errorf("model returned an empty response")
			}
			return resp.Content, nil
		}

		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		for _, call := range resp.ToolCalls {
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    a.executeToolAsJSON(ctx, call, a.isOnline(ctx)),
				Name:       call.Function.Name,
				ToolCallID: call.ID,
			})
		}
	}

	return "", fmt.Errorf("exceeded %d tool-call iterations without a final answer", maxToolIterations)
}

// executeToolAsJSON runs a tool call and returns its result (or error)
// marshaled to JSON, since a "tool" message's content must be a string.
func (a *Agent) executeToolAsJSON(ctx context.Context, call llm.ToolCall, online bool) string {
	result, err := a.executeTool(ctx, call, online)
	if err != nil {
		a.errLog.Record("tool_call", call.Function.Name, err)
		result = map[string]any{"error": err.Error()}
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(payload)
}

func (a *Agent) executeTool(ctx context.Context, call llm.ToolCall, online bool) (any, error) {
	tool, ok := a.registry.Get(call.Function.Name)
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", call.Function.Name)
	}
	if !a.registry.IsAvailable(call.Function.Name, online) {
		return nil, fmt.Errorf("tool %s is unavailable while offline", call.Function.Name)
	}

	var args map[string]any
	if call.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("invalid arguments for %s: %w", call.Function.Name, err)
		}
	}

	return tool.Execute(ctx, args)
}

func (a *Agent) toolDefinitions(online bool) []llm.ToolDefinition {
	names := a.registry.AvailableList(online)
	defs := make([]llm.ToolDefinition, 0, len(names))
	for _, name := range names {
		tool, _ := a.registry.Get(name)
		defs = append(defs, llm.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.InputSchema(),
		})
	}
	return defs
}

func (a *Agent) isOnline(ctx context.Context) bool {
	if a.networkAvailability == nil {
		return true
	}
	return a.networkAvailability(ctx)
}

// providerNamer is implemented by llm.Router (CurrentProvider). It's an
// optional capability, checked with a type assertion, so a bare llm.Client
// used directly in tests doesn't need to implement it.
type providerNamer interface {
	CurrentProvider() string
}

func (a *Agent) chatProvider() string {
	if namer, ok := a.client.(providerNamer); ok {
		return namer.CurrentProvider()
	}
	return "unknown"
}
