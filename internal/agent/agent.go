// Package agent wires an LLM client to a tool registry: it runs the
// conversation loop that calls the model, executes any tool calls it
// requests, feeds the results back, and repeats until a final answer.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/roman220/bosun-smarthelper/internal/errlog"
	"github.com/roman220/bosun-smarthelper/internal/llm"
	"github.com/roman220/bosun-smarthelper/internal/tools"
)

const systemPrompt = `Be concise. Use available tools for live data, sensors, and memos. ` +
	`A sensor reading (GPS, weather, fridge, system stats) from earlier in this conversation is stale the moment time passes — ` +
	`never answer a live-data question from a value already in the conversation history; always call the tool again for a fresh reading. ` +
	`Never delete a memo unless explicitly asked; archive old ones instead. ` +
	`For mountain weather use a named mountain, park, or pass, never a nearby city; clarify ambiguity. ` +
	`Try memo before web_search for anything the user's own uploads might cover (their vehicle/equipment, procedures, diagrams) — ` +
	`fall back to web_search only if memo comes back empty. ` +
	`Don't narrate or acknowledge your own instructions (language, style, persona); just answer directly.`

// maxToolIterations bounds the tool-call loop so a misbehaving model can't
// spin forever.
const maxToolIterations = 5

// maxEmptyResponseRetries bounds how many times AskWithHistoryStreaming
// retries a call that returned no tool calls and no content — a rare but
// real provider quirk (confirmed live: a reasoning model whose response
// was entirely consumed by its own <think> preamble before hitting the
// token limit, leaving nothing else behind — see llm.Response.FinishReason).
// The identical request is simply resent; nothing about the conversation
// changed, so there's nothing to fix before trying again.
const maxEmptyResponseRetries = 2

// ChatClient is the subset of llm.Client (or llm.Router) the agent needs.
type ChatClient interface {
	Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition) (*llm.Response, error)
}

// HistoryMessage is a completed user or assistant message retained between
// turns. Internal tool protocol messages are intentionally not persisted.
//
// DurationMS/*Tokens/Model are only ever set on an assistant message, and
// only carry what TurnStats reported for it (see internal/webui/server.go's
// saveAssistantReply) — buildMessages below only reads Role/Content when
// replaying history back to the LLM, so these fields ride along purely for
// internal/webui's GET /api/history to restore the ℹ️ stats icon after a
// page reload, without needing a separate persisted store.
type HistoryMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	DurationMS       int64  `json:"duration_ms,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
	Model            string `json:"model,omitempty"`
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

// StepEvent is emitted during a streaming conversation turn. Type is
// "step_start" (a new LLM call in the loop is beginning — the caller should
// start a new bubble/message) or "delta" (Delta carries the incremental
// text; see llm.StreamDelta for what Kind means).
type StepEvent struct {
	Type  string
	Delta llm.StreamDelta
}

// TurnStats summarizes one Ask* call: aggregate token usage across every
// LLM call the turn's tool-loop made, plus which model actually answered.
// Model is whatever the provider's own response reported (llm.Response.Model)
// — for a local provider this is normally the real model identity; for a
// provider sitting behind a generic alias (this deployment's remote proxy
// uses "text"/"coding"/etc.) it's just the alias. BackendModel, when
// non-empty, is what llm.Response.BackendModel reported instead — a more
// specific identity the proxy chose to reveal via a response header, not
// derivable from the alias alone.
type TurnStats struct {
	Usage        llm.Usage
	Model        string
	BackendModel string
}

// DisplayModel is the more specific of Model/BackendModel — prefer
// BackendModel when the provider reported one, since Model alone may just
// be a generic alias.
func (s TurnStats) DisplayModel() string {
	if s.BackendModel != "" {
		return s.BackendModel
	}
	return s.Model
}

// Ask sends a single user message through the conversation loop, executing
// any tool calls the model requests, and returns its final text answer
// plus TurnStats — aggregate token usage and which model answered, summed/
// taken across every LLM call the turn made (see AskWithHistoryStreaming).
func (a *Agent) Ask(ctx context.Context, userMessage string) (string, TurnStats, error) {
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
) (string, TurnStats, error) {
	return a.AskWithHistoryStreaming(ctx, userMessage, history, language, nil)
}

// AskWithHistoryStreaming is AskWithHistory with an additional callback: if
// the underlying client supports streaming (llm.StreamingClient, checked
// via type assertion), onEvent is called with a "step_start" at the
// beginning of each LLM call in the loop and a "delta" for each incremental
// piece of text, including a synthetic fold delta once a tool call's result
// is known. onEvent may be nil (AskWithHistory does exactly that), and a
// client that doesn't support streaming still works — its complete response
// is delivered as one "delta" event instead.
//
// The returned TurnStats.Usage is the sum of every LLM call's usage this
// turn made — a turn can involve several (the tool-loop below), and a
// caller asking "how many tokens did this turn cost" wants the total, not
// just the last call's. Model/BackendModel are instead taken from the
// *last* successful call, since every call in one turn shares the same
// configured provider/model — there's nothing to sum there. TurnStats
// reflects whatever was accumulated even on an error return, since real
// calls that actually happened still cost real tokens and did answer from
// some real model.
func (a *Agent) AskWithHistoryStreaming(
	ctx context.Context,
	userMessage string,
	history []HistoryMessage,
	language string,
	onEvent func(StepEvent),
) (string, TurnStats, error) {
	online := a.isOnline(ctx)
	toolDefs := a.toolDefinitions(online)
	messages := a.buildMessages(userMessage, history, language, online)

	// A separate cancellable context so a detected repetition loop (below)
	// can abort the in-flight request instead of just hiding its output —
	// otherwise a degenerate model keeps burning tokens/time after the user
	// has already stopped seeing anything new.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	streamer, canStream := a.client.(llm.StreamingClient)
	var detector repetitionDetector
	var forwardedProse strings.Builder
	var truncated bool
	var stats TurnStats
	onDelta := func(d llm.StreamDelta) {
		if truncated {
			return
		}
		if d.Kind == "prose" {
			if detector.feed(d.Text) {
				truncated = true
				cancelStream()
				return
			}
			forwardedProse.WriteString(d.Text)
		}
		if onEvent != nil {
			onEvent(StepEvent{Type: "delta", Delta: d})
		}
	}

	var emptyResponseRetries int
	var lastFinishReason string
	for i := 0; i < maxToolIterations; i++ {
		if onEvent != nil {
			onEvent(StepEvent{Type: "step_start"})
		}
		detector = repetitionDetector{}
		forwardedProse.Reset()
		truncated = false

		var resp *llm.Response
		var err error
		if canStream {
			resp, err = streamer.ChatStream(streamCtx, messages, toolDefs, onDelta)
		} else {
			resp, err = a.client.Chat(streamCtx, messages, toolDefs)
			if err == nil && resp.Content != "" {
				onDelta(llm.StreamDelta{Kind: "prose", Text: resp.Content})
			}
		}
		if truncated {
			// Not a real failure — whatever coherent prose was already
			// forwarded before the model collapsed into repetition is the
			// answer; there's nothing further worth waiting for from a
			// response that's already degenerated. resp may be nil or
			// incomplete (the stream was cancelled mid-flight), so only
			// prior iterations' stats are reported here.
			return forwardedProse.String(), stats, nil
		}
		if err != nil {
			// A cancelled or expired context (user hit "stop", or the request
			// timeout fired) isn't a bug to track — only record genuine
			// provider failures.
			if ctx.Err() == nil {
				a.errLog.Record("llm_chat", a.chatProvider(), err)
			}
			return "", stats, fmt.Errorf("chat: %w", err)
		}
		stats.Usage.PromptTokens += resp.Usage.PromptTokens
		stats.Usage.CompletionTokens += resp.Usage.CompletionTokens
		stats.Usage.TotalTokens += resp.Usage.TotalTokens
		if resp.Model != "" {
			stats.Model = resp.Model
		}
		if resp.BackendModel != "" {
			stats.BackendModel = resp.BackendModel
		}

		if len(resp.ToolCalls) == 0 {
			if strings.TrimSpace(resp.Content) == "" {
				if resp.FinishReason != "" {
					lastFinishReason = resp.FinishReason
				}
				if emptyResponseRetries < maxEmptyResponseRetries {
					// Same messages, nothing to fix — a transient empty
					// completion isn't evidence anything about the request
					// itself was wrong. Doesn't reset detector/forwardedProse
					// specially; the top of the next iteration already does.
					emptyResponseRetries++
					continue
				}
				err := fmt.Errorf("model returned an empty response after %d retries (last finish_reason: %q)", emptyResponseRetries, lastFinishReason)
				if ctx.Err() == nil {
					a.errLog.Record("llm_chat", a.chatProvider(), err)
				}
				return "", stats, err
			}
			return resp.Content, stats, nil
		}

		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		for _, call := range resp.ToolCalls {
			result := a.executeToolAsJSON(ctx, call, a.isOnline(ctx))
			onDelta(llm.StreamDelta{Kind: "fold", Text: "\n→ " + result})
			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    result,
				Name:       call.Function.Name,
				ToolCallID: call.ID,
			})
		}
	}

	return "", stats, fmt.Errorf("exceeded %d tool-call iterations without a final answer", maxToolIterations)
}

func (a *Agent) buildMessages(userMessage string, history []HistoryMessage, language string, online bool) []llm.Message {
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
	return messages
}

// executeToolAsJSON runs a tool call and returns its result (or error)
// marshaled to JSON, since a "tool" message's content must be a string.
func (a *Agent) executeToolAsJSON(ctx context.Context, call llm.ToolCall, online bool) string {
	result, err := a.executeTool(ctx, call, online)
	if err != nil {
		if ctx.Err() == nil {
			a.errLog.Record("tool_call", call.Function.Name, err)
		}
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
