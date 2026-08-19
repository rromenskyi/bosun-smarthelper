package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/errlog"
	"github.com/roman220/ai-local-smarthelper/internal/llm"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

// fakeClient replays a fixed sequence of responses, one per Chat call, and
// records the messages it was called with. If shouldFail is set, the call
// at index failOnCall returns an error instead of consuming a response.
type fakeClient struct {
	responses  []*llm.Response
	calls      int
	seen       [][]llm.Message
	seenTools  [][]llm.ToolDefinition
	shouldFail bool
	failOnCall int
}

func (f *fakeClient) Chat(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition) (*llm.Response, error) {
	f.seen = append(f.seen, messages)
	f.seenTools = append(f.seenTools, toolDefs)
	if f.shouldFail && f.calls == f.failOnCall {
		f.calls++
		return nil, errors.New("simulated chat failure")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

// fakeStreamingClient replays responses like fakeClient but also implements
// llm.StreamingClient, emitting each response's Content as a single
// synthetic prose delta — enough to test agent-level event sequencing
// without a real SSE/NDJSON fixture (that's covered in internal/llm).
type fakeStreamingClient struct {
	fakeClient
}

func (f *fakeStreamingClient) ChatStream(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition, onDelta func(llm.StreamDelta)) (*llm.Response, error) {
	resp, err := f.Chat(ctx, messages, toolDefs)
	if err != nil {
		return nil, err
	}
	if resp.Content != "" {
		onDelta(llm.StreamDelta{Kind: "prose", Text: resp.Content})
	}
	return resp, nil
}

func TestAgent_AskWithHistoryStreaming_EmitsStepAndDeltaEvents(t *testing.T) {
	toolCall := llm.ToolCall{ID: "call_1", Type: "function"}
	toolCall.Function.Name = "get_weather"
	toolCall.Function.Arguments = "{}"

	client := &fakeStreamingClient{fakeClient: fakeClient{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall}},
		{Content: "It's 21.5°C outside."},
	}}}

	registry := tools.NewRegistry()
	registry.Register(tools.NewWeatherTool(&config.WeatherConfig{Type: "mock", MockTempC: 21.5, MockHumidity: 50}))
	ag := New(client, registry)

	var events []StepEvent
	answer, err := ag.AskWithHistoryStreaming(context.Background(), "weather?", nil, "", func(e StepEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("AskWithHistoryStreaming returned error: %v", err)
	}
	if answer != "It's 21.5°C outside." {
		t.Errorf("answer = %q", answer)
	}

	wantTypes := []string{"step_start", "delta", "step_start", "delta"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v, want %d events of types %v", events, len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("event %d type = %q, want %q", i, events[i].Type, want)
		}
	}
	if events[1].Delta.Kind != "fold" || !strings.Contains(events[1].Delta.Text, "temperature_c") {
		t.Errorf("tool-result delta = %+v, want a fold delta containing the tool's JSON result", events[1].Delta)
	}
	if events[3].Delta.Kind != "prose" || events[3].Delta.Text != "It's 21.5°C outside." {
		t.Errorf("final delta = %+v", events[3].Delta)
	}
}

// chunkedStreamingClient emits Content one caller-supplied piece at a time
// (unlike fakeStreamingClient's single whole-content delta) so tests can
// exercise mid-stream behavior — here, the repetition cutoff — the way a
// real SSE/NDJSON stream actually arrives, a few characters at a time.
// Stops delivering further chunks once ctx is cancelled, mirroring how a
// real HTTP stream read aborts.
type chunkedStreamingClient struct {
	chunks []string
	resp   *llm.Response
}

func (f *chunkedStreamingClient) Chat(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition) (*llm.Response, error) {
	return f.resp, nil
}

func (f *chunkedStreamingClient) ChatStream(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDefinition, onDelta func(llm.StreamDelta)) (*llm.Response, error) {
	for _, chunk := range f.chunks {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		onDelta(llm.StreamDelta{Kind: "prose", Text: chunk})
	}
	return f.resp, nil
}

func TestAgent_AskWithHistoryStreaming_TruncatesRunawayRepetition(t *testing.T) {
	const garbageToken = "<pad>"
	chunks := []string{"Всё в норме, капитан. "}
	for i := 0; i < 20; i++ {
		chunks = append(chunks, garbageToken)
	}
	client := &chunkedStreamingClient{chunks: chunks, resp: &llm.Response{Content: strings.Join(chunks, "")}}
	ag := New(client, tools.NewRegistry())

	var delivered strings.Builder
	answer, err := ag.AskWithHistoryStreaming(context.Background(), "как дела?", nil, "", func(e StepEvent) {
		if e.Type == "delta" && e.Delta.Kind == "prose" {
			delivered.WriteString(e.Delta.Text)
		}
	})
	if err != nil {
		t.Fatalf("AskWithHistoryStreaming returned error: %v", err)
	}
	if !strings.Contains(answer, "Всё в норме") {
		t.Errorf("answer lost the coherent prefix: %q", answer)
	}
	if got := strings.Count(answer, garbageToken); got == 0 || got > repetitionMinRepeats {
		t.Errorf("answer has %d copies of %q, want a small bounded number (>0, <=%d), not all 20 nor zero: %q",
			got, garbageToken, repetitionMinRepeats, answer)
	}
	if got := strings.Count(delivered.String(), garbageToken); got > repetitionMinRepeats {
		t.Errorf("streamed events delivered %d copies of %q, want cut off at <=%d", got, garbageToken, repetitionMinRepeats)
	}
}

func TestAgent_AskWithHistory_NilEventCallbackStillWorks(t *testing.T) {
	// AskWithHistory delegates to AskWithHistoryStreaming(onEvent: nil) — a
	// client that DOES implement StreamingClient must still behave
	// correctly when nobody's listening for events.
	client := &fakeStreamingClient{fakeClient: fakeClient{responses: []*llm.Response{{Content: "Hello there."}}}}
	ag := New(client, tools.NewRegistry())

	answer, err := ag.Ask(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if answer != "Hello there." {
		t.Errorf("answer = %q", answer)
	}
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

func TestAgent_RecordsToolAndChatFailuresToErrorLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "errors.jsonl")
	errLog, err := errlog.Open(logPath)
	if err != nil {
		t.Fatalf("errlog.Open: %v", err)
	}
	defer errLog.Close()

	toolCall := llm.ToolCall{ID: "call_1", Type: "function"}
	toolCall.Function.Name = "does_not_exist"
	client := &fakeClient{
		responses:  []*llm.Response{{ToolCalls: []llm.ToolCall{toolCall}}},
		shouldFail: true,
		failOnCall: 1, // the second Chat call (after the tool result) fails
	}
	ag := New(client, tools.NewRegistry())
	ag.SetErrorLog(errLog)

	if _, err := ag.Ask(context.Background(), "do the impossible"); err == nil {
		t.Fatal("expected an error from the failing second Chat call")
	}
	errLog.Close()

	entries, err := errlog.ReadAll(logPath)
	if err != nil {
		t.Fatalf("errlog.ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %#v", len(entries), entries)
	}
	if entries[0].Category != "tool_call" || entries[0].Detail != "does_not_exist" {
		t.Errorf("entry 0 = %#v, want tool_call/does_not_exist", entries[0])
	}
	if entries[1].Category != "llm_chat" {
		t.Errorf("entry 1 = %#v, want category llm_chat", entries[1])
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
