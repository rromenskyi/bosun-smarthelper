package llm

import (
	"strings"
	"testing"
)

func TestParseOpenAISSEStream_PlainProse(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"model":"text","choices":[{"delta":{"content":"Hello, "},"finish_reason":null}]}`,
		`data: {"model":"text","choices":[{"delta":{"content":"world!"},"finish_reason":null}]}`,
		`data: [DONE]`,
		"",
	}, "\n"))

	var deltas []StreamDelta
	response, err := parseOpenAISSEStream(body, func(d StreamDelta) { deltas = append(deltas, d) }, false)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}
	if response.Content != "Hello, world!" {
		t.Errorf("content = %q", response.Content)
	}
	if len(deltas) != 2 || deltas[0].Kind != "prose" || deltas[1].Kind != "prose" {
		t.Fatalf("deltas = %#v, want two prose deltas", deltas)
	}
}

func TestParseOpenAISSEStream_CapturesFinalUsageChunk(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"model":"text","choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}`,
		// The real final chunk when stream_options.include_usage was set:
		// empty choices, top-level usage.
		`data: {"model":"text","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`,
		`data: [DONE]`,
		"",
	}, "\n"))

	response, err := parseOpenAISSEStream(body, func(StreamDelta) {}, false)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}
	if response.Content != "Hi" {
		t.Errorf("content = %q", response.Content)
	}
	want := Usage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 15}
	if response.Usage != want {
		t.Errorf("usage = %+v, want %+v", response.Usage, want)
	}
}

func TestParseOpenAISSEStream_NoUsageChunkLeavesZeroUsage(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"model":"text","choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`data: [DONE]`,
		"",
	}, "\n"))

	response, err := parseOpenAISSEStream(body, func(StreamDelta) {}, false)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}
	if response.Usage != (Usage{}) {
		t.Errorf("usage = %+v, want zero value when the server never sends one", response.Usage)
	}
}

func TestParseOpenAISSEStream_ToolCallsReassembled(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"model":"text","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"loc"}}]}}]}`,
		`data: {"model":"text","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"Denver\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n"))

	response, err := parseOpenAISSEStream(body, nil, false)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "get_weather" {
		t.Errorf("call = %+v", call)
	}
	if call.Function.Arguments != `{"location":"Denver"}` {
		t.Errorf("arguments = %q, want fragments concatenated in order", call.Function.Arguments)
	}
}

func TestParseOpenAISSEStream_FoldDetectionAfterProse(t *testing.T) {
	// Mirrors the exact fixture from TestParseLlamaToolCalls: prose, then a
	// tool call, split across several small chunks the way real token-level
	// streaming would deliver it — including splitting the marker itself.
	chunks := []string{
		"I will check.\n<too",
		"l_call>\n<function=get_weather>\n<parameter=location>\n",
		"Denver\n</parameter>\n</function>\n</tool_call>",
	}
	var lines []string
	for _, c := range chunks {
		encoded := strings.ReplaceAll(c, "\n", `\n`)
		lines = append(lines, `data: {"model":"text","choices":[{"delta":{"content":"`+encoded+`"}}]}`)
	}
	lines = append(lines, "data: [DONE]", "")
	body := strings.NewReader(strings.Join(lines, "\n"))

	var deltas []StreamDelta
	response, err := parseOpenAISSEStream(body, func(d StreamDelta) { deltas = append(deltas, d) }, true)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}

	var prose, fold strings.Builder
	for _, d := range deltas {
		switch d.Kind {
		case "prose":
			prose.WriteString(d.Text)
		case "fold":
			fold.WriteString(d.Text)
		default:
			t.Errorf("unexpected delta kind %q", d.Kind)
		}
	}
	if prose.String() != "I will check.\n" {
		t.Errorf("prose = %q, want the text before the marker, marker excluded", prose.String())
	}
	if !strings.HasPrefix(fold.String(), "<tool_call>") {
		t.Errorf("fold = %q, want it to start with the (reassembled) marker", fold.String())
	}

	// The full raw text is still preserved in Content for parseLlamaToolCalls
	// to extract from afterward — nothing is dropped, only reclassified.
	if !strings.Contains(response.Content, "<tool_call>") {
		t.Errorf("content = %q, want the full raw text retained", response.Content)
	}
}

func TestParseOpenAISSEStream_StripsLeakedReasoningMarkerBeforeFirstFlush(t *testing.T) {
	body := strings.NewReader(
		`data: {"model":"text","choices":[{"delta":{"content":"0thought\n<channel|>Слушаюсь, капитан! Вот длинный ответ, чтобы окно проверки точно набралось."}}]}` + "\n" +
			"data: [DONE]\n",
	)

	var deltas []StreamDelta
	response, err := parseOpenAISSEStream(body, func(d StreamDelta) { deltas = append(deltas, d) }, true)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}

	var shown strings.Builder
	for _, d := range deltas {
		shown.WriteString(d.Text)
	}
	if strings.Contains(shown.String(), "<channel|>") || strings.Contains(shown.String(), "0thought") {
		t.Errorf("shown text = %q, leaked marker must never reach onDelta", shown.String())
	}
	if strings.HasPrefix(shown.String(), " ") {
		t.Errorf("shown text = %q, should not have leading whitespace after stripping", shown.String())
	}

	// stripLeakedReasoningMarker (called by the caller on the assembled
	// Response, mirroring the non-streaming path) must produce the same
	// result the streamed deltas already showed.
	stripLeakedReasoningMarker(response)
	if response.Content != shown.String() {
		t.Errorf("response.Content = %q after stripping, want it to match what was streamed (%q)", response.Content, shown.String())
	}
}

func TestFoldDetector_MarkerNeverAppears(t *testing.T) {
	d := &foldDetector{}
	var deltas []StreamDelta
	onDelta := func(delta StreamDelta) { deltas = append(deltas, delta) }

	for _, chunk := range []string{"Всё ", "спокойно, ", "капитан, ", "горизонт ", "чист.", " Ничего похожего на маркер тут нет вообще."} {
		d.feed(chunk, onDelta)
	}
	d.flush(onDelta)

	var got strings.Builder
	for _, delta := range deltas {
		if delta.Kind != "prose" {
			t.Errorf("unexpected fold delta when no marker ever appeared: %+v", delta)
		}
		got.WriteString(delta.Text)
	}
	want := "Всё спокойно, капитан, горизонт чист. Ничего похожего на маркер тут нет вообще."
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}
