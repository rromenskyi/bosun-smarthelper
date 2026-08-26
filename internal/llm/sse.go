package llm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// foldDetector classifies streamed text as "prose" or "fold" for a source
// that may embed a <tool_call> marker directly in otherwise-ordinary
// content (the OpenAI-compatible local client — see parseLlamaToolCalls).
// It holds back a small tail of unflushed text so a marker split across
// two chunks is still detected before any of it is shown to the user, and
// applies the same one-time leaked-reasoning-marker check as
// stripLeakedReasoningMarker before the first flush, so that leak never
// gets a chance to appear on screen either.
type foldDetector struct {
	pending      string
	foldMode     bool
	strippedLead bool
}

// leadCheckWindow must be large enough that leakedReasoningMarkerPattern
// (bounded at 32 chars of garbage plus the marker itself) can always be
// evaluated with confidence before the first flush.
const leadCheckWindow = 48

func (d *foldDetector) feed(newText string, onDelta func(StreamDelta)) {
	if d.foldMode {
		if onDelta != nil && newText != "" {
			onDelta(StreamDelta{Kind: "fold", Text: newText})
		}
		return
	}

	d.pending += newText

	if !d.strippedLead {
		if len(d.pending) < leadCheckWindow {
			return // wait for more text before deciding
		}
		d.stripLead()
	}

	const marker = "<tool_call>"
	if idx := strings.Index(d.pending, marker); idx != -1 {
		if prose := d.pending[:idx]; prose != "" && onDelta != nil {
			onDelta(StreamDelta{Kind: "prose", Text: prose})
		}
		fold := d.pending[idx:]
		d.pending = ""
		d.foldMode = true
		if onDelta != nil && fold != "" {
			onDelta(StreamDelta{Kind: "fold", Text: fold})
		}
		return
	}

	// Keep the last len(marker)-1 bytes unflushed in case they're the start
	// of a marker split across this chunk and the next one.
	safeLen := len(d.pending) - (len(marker) - 1)
	if safeLen > 0 {
		if onDelta != nil {
			onDelta(StreamDelta{Kind: "prose", Text: d.pending[:safeLen]})
		}
		d.pending = d.pending[safeLen:]
	}
}

func (d *foldDetector) stripLead() {
	if loc := leakedReasoningMarkerPattern.FindStringIndex(d.pending); loc != nil {
		d.pending = d.pending[loc[1]:]
	}
	d.strippedLead = true
}

// flush emits any text still held back once the stream has ended.
func (d *foldDetector) flush(onDelta func(StreamDelta)) {
	if !d.strippedLead {
		d.stripLead()
	}
	if d.pending == "" || onDelta == nil {
		d.pending = ""
		return
	}
	kind := "prose"
	if d.foldMode {
		kind = "fold"
	}
	onDelta(StreamDelta{Kind: kind, Text: d.pending})
	d.pending = ""
}

type openAIStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string                 `json:"content"`
			ToolCalls []openAIStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	// Usage is only present on the final chunk, and only when the request
	// set stream_options.include_usage — see openAIStreamOptions. That
	// chunk's Choices is empty, so this must be read before the
	// len(chunk.Choices) == 0 skip below, not after.
	Usage *Usage `json:"usage"`
}

type toolCallAccumulator struct {
	id        string
	typ       string
	name      string
	arguments strings.Builder
}

// parseOpenAISSEStream reads an OpenAI-compatible SSE stream ("data: {...}"
// lines terminated by "data: [DONE]"), forwarding content deltas via
// onDelta and returning the fully assembled Response once the stream ends.
// When detectFold is true, content is run through a foldDetector (the
// OpenAI-compatible local client, whose raw content can contain a leaked
// <tool_call> block); when false, content is always "prose" (the real
// remote provider, whose tool calls always arrive as a structured field).
func parseOpenAISSEStream(body io.Reader, onDelta func(StreamDelta), detectFold bool) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var content strings.Builder
	var detector *foldDetector
	if detectFold {
		detector = &foldDetector{}
	}

	toolCalls := make(map[int]*toolCallAccumulator)
	var order []int
	var model string
	var usage Usage

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // skip a malformed chunk rather than fail the whole stream
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			content.WriteString(delta.Content)
			if detector != nil {
				detector.feed(delta.Content, onDelta)
			} else if onDelta != nil {
				onDelta(StreamDelta{Kind: "prose", Text: delta.Content})
			}
		}

		for _, tc := range delta.ToolCalls {
			acc, ok := toolCalls[tc.Index]
			if !ok {
				acc = &toolCallAccumulator{}
				toolCalls[tc.Index] = acc
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Type != "" {
				acc.typ = tc.Type
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			acc.arguments.WriteString(tc.Function.Arguments)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if detector != nil {
		detector.flush(onDelta)
	}

	response := &Response{Content: content.String(), Model: model, Usage: usage}
	sort.Ints(order)
	for _, idx := range order {
		acc := toolCalls[idx]
		call := ToolCall{ID: acc.id, Type: acc.typ}
		if call.Type == "" {
			call.Type = "function"
		}
		call.Function.Name = acc.name
		call.Function.Arguments = acc.arguments.String()
		response.ToolCalls = append(response.ToolCalls, call)
	}
	return response, nil
}
