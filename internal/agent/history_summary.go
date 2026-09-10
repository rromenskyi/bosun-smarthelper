package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/roman220/bosun-smarthelper/internal/llm"
)

// HistorySummaryConfig holds the three tunable knobs behind conversation
// history compaction — see settings.Data's HistorySummary* fields for
// why this exists at all (a long-running chat sending its full,
// ever-growing history on every turn has caused real, live failures:
// an 8192-token remote backend flatly rejecting an oversized request,
// and separately an oversized prompt just taking too long, especially
// on the CPU-bound local model). Zero in any field means "use the
// built-in default" (see the const block below), never "off" — an
// unbounded history isn't a safe default to opt out of.
type HistorySummaryConfig struct {
	HeadTokens      int
	TailTokens      int
	ThresholdTokens int
}

const (
	defaultHistorySummaryHeadTokens      = 1500
	defaultHistorySummaryTailTokens      = 2000
	defaultHistorySummaryThresholdTokens = 6000
)

// charsPerTokenEstimate approximates tokens from character count when no
// real API-reported count is available yet. Deliberately a slight
// overestimate for English (real BPE tokenizers average closer to 4
// chars/token there) and a reasonable one for Cyrillic (which tends to
// run fewer chars per token) — erring toward compacting a bit early
// rather than late is the safe direction here.
const charsPerTokenEstimate = 3

func estimateMessageTokens(m HistoryMessage) int {
	return len(m.Content) / charsPerTokenEstimate
}

// estimateTokens approximates history's total token cost for the LLM.
// Rather than guessing at everything by character count, it uses the
// most recent real API-reported PromptTokens (already sitting on
// whichever assistant message it was recorded against — see
// HistoryMessage) as an exact, free baseline for everything up to and
// including that message, and only estimates by character count for
// whatever's newer than that (a real count for it doesn't exist yet).
func estimateTokens(history []HistoryMessage) int {
	baseIndex := -1
	base := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].PromptTokens > 0 {
			baseIndex = i
			base = history[i].PromptTokens
			break
		}
	}
	// base covers everything strictly before baseIndex (that's what a
	// PromptTokens value recorded on it represents — the prompt sent to
	// generate it) — baseIndex's own content still needs adding.
	start := 0
	if baseIndex >= 0 {
		start = baseIndex
	}
	total := base
	for i := start; i < len(history); i++ {
		total += estimateMessageTokens(history[i])
	}
	return total
}

// tokenBoundaryFromStart returns how many messages, counting from index
// 0, fit within budget — the boundary where the "head" (kept verbatim)
// ends. Always at least 1 (for a non-empty history), so a single huge
// leading message doesn't collapse the head to nothing.
func tokenBoundaryFromStart(history []HistoryMessage, budget int) int {
	used := 0
	for i, m := range history {
		cost := estimateMessageTokens(m)
		if used+cost > budget && i > 0 {
			return i
		}
		used += cost
	}
	return len(history)
}

// tokenBoundaryFromEnd returns the index where the "tail" (kept
// verbatim, counting backward from the end) begins. Always leaves at
// least 1 message in the tail, for the same reason as
// tokenBoundaryFromStart.
func tokenBoundaryFromEnd(history []HistoryMessage, budget int) int {
	used := 0
	for i := len(history) - 1; i >= 0; i-- {
		cost := estimateMessageTokens(history[i])
		if used+cost > budget && i < len(history)-1 {
			return i + 1
		}
		used += cost
	}
	return 0
}

// CompactHistory returns the history to actually send to the LLM this
// turn. Below cfg's threshold, history is returned unchanged — this is
// the common case, and the only one before a conversation grows large.
// Past it, the shape becomes head (verbatim) + one synthetic summary
// message + tail (verbatim): full detail at both ends (the original
// framing of the conversation, and everything recent enough to matter
// right now), everything in between folded into prose.
//
// Only the portion added since summarizedThroughIndex is ever actually
// summarized — combined with existingSummary via one LLM call — so a
// long-running conversation's summarization cost stays bounded per turn
// instead of re-reading everything from scratch each time.
//
// Returns the effective history to send, the (possibly unchanged)
// summary text, and the (possibly unchanged) index it now covers
// through. internal/webui owns session persistence and is responsible
// for storing these back onto the session so the next call picks up
// from here.
func (a *Agent) CompactHistory(
	ctx context.Context,
	history []HistoryMessage,
	existingSummary string,
	summarizedThroughIndex int,
	cfg HistorySummaryConfig,
) (effective []HistoryMessage, newSummary string, newSummarizedThroughIndex int, err error) {
	head := cfg.HeadTokens
	if head <= 0 {
		head = defaultHistorySummaryHeadTokens
	}
	tail := cfg.TailTokens
	if tail <= 0 {
		tail = defaultHistorySummaryTailTokens
	}
	threshold := cfg.ThresholdTokens
	if threshold <= 0 {
		threshold = defaultHistorySummaryThresholdTokens
	}

	if estimateTokens(history) <= threshold {
		return history, existingSummary, summarizedThroughIndex, nil
	}

	headEnd := tokenBoundaryFromStart(history, head)
	tailStart := tokenBoundaryFromEnd(history, tail)
	if tailStart <= headEnd {
		// Head and tail already meet or overlap — there's no meaningful
		// middle left to fold away, so sending everything as-is (even
		// though it's over threshold) beats manufacturing a summary of
		// zero or negative-length input.
		return history, existingSummary, summarizedThroughIndex, nil
	}

	summarizeFrom := summarizedThroughIndex
	if summarizeFrom < headEnd {
		summarizeFrom = headEnd
	}
	if summarizeFrom >= tailStart {
		// Everything up to the current tail boundary was already folded
		// into existingSummary on an earlier turn (the tail grew to
		// cover what used to be freshly-summarized middle) — nothing new
		// to summarize this time.
		newSummary = existingSummary
		newSummarizedThroughIndex = summarizedThroughIndex
	} else {
		newSummary, err = a.summarizeMessages(ctx, existingSummary, history[summarizeFrom:tailStart])
		if err != nil {
			return history, existingSummary, summarizedThroughIndex, fmt.Errorf("summarize history: %w", err)
		}
		newSummarizedThroughIndex = tailStart
	}

	effective = make([]HistoryMessage, 0, headEnd+1+(len(history)-tailStart))
	effective = append(effective, history[:headEnd]...)
	effective = append(effective, HistoryMessage{
		Role: "assistant",
		Content: "[Summary of earlier conversation, kept for context. The full original " +
			"is still on record — use search_history if the user references exact " +
			"wording or details from it you don't see here.]\n" + newSummary,
	})
	effective = append(effective, history[tailStart:]...)
	return effective, newSummary, newSummarizedThroughIndex, nil
}

// summarizeMessages asks the model itself to fold existingSummary plus
// messages into one updated summary — a single plain completion, no
// tools, no conversation history of its own.
func (a *Agent) summarizeMessages(ctx context.Context, existingSummary string, messages []HistoryMessage) (string, error) {
	var b strings.Builder
	if existingSummary != "" {
		b.WriteString("Existing summary of the conversation so far:\n")
		b.WriteString(existingSummary)
		b.WriteString("\n\n")
	}
	b.WriteString("New messages since then:\n")
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}

	prompt := "Summarize the conversation below concisely but completely — preserve every " +
		"concrete fact, decision, number, name, and open task; drop only pleasantries and " +
		"repeated phrasing. Write it as prose, third person, no preamble, no headers.\n\n" + b.String()

	resp, err := a.client.Chat(ctx, []llm.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
