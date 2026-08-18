package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/llm"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

// fakeClient replays a fixed sequence of responses, one per Chat call, and
// records the messages it was called with.
type fakeClient struct {
	responses []*llm.Response
	calls     int
	seen      [][]llm.Message
	seenTools [][]llm.ToolDefinition
}

func (f *fakeClient) Chat(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition) (*llm.Response, error) {
	f.seen = append(f.seen, messages)
	f.seenTools = append(f.seenTools, toolDefs)
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

func TestAgent_Ask_HidesNetworkToolsOffline(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{{Content: "Offline."}}}
	registry := tools.NewRegistry()
	registry.Register(tools.NewWeatherTool(&config.WeatherConfig{Type: "open_meteo"}))
	registry.Register(tools.NewGPSTool(&config.GPSConfig{Type: "mock"}))
	ag := New(client, registry, func(context.Context) bool { return false })

	if _, err := ag.Ask(context.Background(), "help"); err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if len(client.seenTools) != 1 || len(client.seenTools[0]) != 1 {
		t.Fatalf("offline tool definitions = %#v, want one local tool", client.seenTools)
	}
	if got := client.seenTools[0][0].Name; got != "get_gps" {
		t.Errorf("offline tool = %q, want get_gps", got)
	}
	if !strings.Contains(client.seen[0][0].Content, "Offline: do not claim live internet access") {
		t.Errorf("offline system prompt missing connectivity guidance: %q", client.seen[0][0].Content)
	}
}

func TestAgent_Ask_NoToolCall(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		{Content: "Hello there."},
	}}
	ag := New(client, tools.NewRegistry())

	answer, err := ag.Ask(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if answer != "Hello there." {
		t.Errorf("answer = %q, want %q", answer, "Hello there.")
	}
	if client.calls != 1 {
		t.Errorf("calls = %d, want 1", client.calls)
	}
}

func TestAgent_AskWithHistory(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{{Content: "Your name is Roman."}}}
	ag := New(client, tools.NewRegistry())
	history := []HistoryMessage{
		{Role: "user", Content: "My name is Roman."},
		{Role: "assistant", Content: "Nice to meet you."},
		{Role: "tool", Content: "must be ignored"},
	}
	if _, err := ag.AskWithHistory(context.Background(), "What is my name?", history, "ru"); err != nil {
		t.Fatalf("AskWithHistory returned error: %v", err)
	}
	messages := client.seen[0]
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4: %#v", len(messages), messages)
	}
	if !strings.Contains(messages[0].Content, "Respond in Russian.") {
		t.Errorf("system prompt = %q, want the language directive folded in, not injected into the user turn", messages[0].Content)
	}
	if messages[1].Role != "user" || messages[1].Content != "My name is Roman." || messages[3].Content != "What is my name?" {
		t.Errorf("unexpected history messages: %#v", messages)
	}
}

func TestAgent_Ask_RejectsEmptyResponse(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{{}}}
	ag := New(client, tools.NewRegistry())
	if _, err := ag.Ask(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error for an empty model response")
	}
}

func TestAgent_Ask_WithToolCall(t *testing.T) {
	toolCall := llm.ToolCall{ID: "call_1", Type: "function"}
	toolCall.Function.Name = "get_weather"
	toolCall.Function.Arguments = "{}"

	client := &fakeClient{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall}},
		{Content: "It's 21.5°C outside."},
	}}

	registry := tools.NewRegistry()
	registry.Register(tools.NewWeatherTool(&config.WeatherConfig{Type: "mock", MockTempC: 21.5, MockHumidity: 50}))
	ag := New(client, registry)

	answer, err := ag.Ask(context.Background(), "what's the weather?")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if answer != "It's 21.5°C outside." {
		t.Errorf("answer = %q, want final content", answer)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2", client.calls)
	}

	secondCallMessages := client.seen[1]
	var foundToolResult bool
	for _, m := range secondCallMessages {
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			foundToolResult = true
			if m.Name != "get_weather" {
				t.Errorf("tool result name = %q, want get_weather", m.Name)
			}
			if m.Content == "" {
				t.Error("tool result message has empty content")
			}
		}
	}
	if !foundToolResult {
		t.Error("expected a tool result message with ToolCallID call_1 in the follow-up request")
	}
}

func TestAgent_Ask_UnknownTool(t *testing.T) {
	toolCall := llm.ToolCall{ID: "call_1", Type: "function"}
	toolCall.Function.Name = "does_not_exist"

	client := &fakeClient{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall}},
		{Content: "I couldn't find that."},
	}}
	ag := New(client, tools.NewRegistry())

	answer, err := ag.Ask(context.Background(), "do the impossible")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if answer != "I couldn't find that." {
		t.Errorf("answer = %q, want graceful fallback", answer)
	}
}

func TestAgent_Ask_ExceedsIterationLimit(t *testing.T) {
	toolCall := llm.ToolCall{ID: "call_loop", Type: "function"}
	toolCall.Function.Name = "get_weather"
	toolCall.Function.Arguments = "{}"

	responses := make([]*llm.Response, 0, maxToolIterations)
	for i := 0; i < maxToolIterations; i++ {
		responses = append(responses, &llm.Response{ToolCalls: []llm.ToolCall{toolCall}})
	}

	client := &fakeClient{responses: responses}
	registry := tools.NewRegistry()
	registry.Register(tools.NewWeatherTool(&config.WeatherConfig{Type: "mock"}))
	ag := New(client, registry)

	if _, err := ag.Ask(context.Background(), "loop forever"); err == nil {
		t.Fatal("expected an error when the model never stops calling tools")
	}
}
