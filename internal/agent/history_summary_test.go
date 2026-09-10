package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/llm"
)

func repeatedHistory(n int, wordsPerMessage int) []HistoryMessage {
	history := make([]HistoryMessage, 0, n)
	word := "word "
	content := strings.Repeat(word, wordsPerMessage)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		history = append(history, HistoryMessage{Role: role, Content: content})
	}
	return history
}

func TestCompactHistoryLeavesSmallHistoryUnchanged(t *testing.T) {
	client := &fakeClient{}
	a := New(client, nil)
	history := repeatedHistory(4, 20) // small, well under any default threshold

	effective, summary, throughIdx, err := a.CompactHistory(context.Background(), history, "", 0, HistorySummaryConfig{})
	if err != nil {
		t.Fatalf("CompactHistory: %v", err)
	}
	if len(effective) != len(history) {
		t.Errorf("effective history length = %d, want unchanged %d", len(effective), len(history))
	}
	if summary != "" || throughIdx != 0 {
		t.Errorf("summary/throughIdx = %q/%d, want unchanged (no compaction should have run)", summary, throughIdx)
	}
	if client.calls != 0 {
		t.Errorf("expected no LLM calls for a history under threshold, got %d", client.calls)
	}
}

func TestCompactHistoryFoldsMiddleIntoSummary(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		{Content: "The user and assistant discussed several unrelated topics."},
	}}
	a := New(client, nil)
	history := repeatedHistory(40, 50) // large enough to force compaction

	cfg := HistorySummaryConfig{HeadTokens: 100, TailTokens: 100, ThresholdTokens: 200}
	effective, summary, throughIdx, err := a.CompactHistory(context.Background(), history, "", 0, cfg)
	if err != nil {
		t.Fatalf("CompactHistory: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one summarization call, got %d", client.calls)
	}
	if summary != "The user and assistant discussed several unrelated topics." {
		t.Errorf("summary = %q", summary)
	}
	if throughIdx <= 0 || throughIdx >= len(history) {
		t.Errorf("summarizedThroughIndex = %d, want strictly between 0 and %d", throughIdx, len(history))
	}
	// effective should be shorter than the original: head + 1 summary
	// message + tail, with the middle collapsed away.
	if len(effective) >= len(history) {
		t.Errorf("effective history length = %d, want shorter than original %d", len(effective), len(history))
	}
	foundSummaryMessage := false
	for _, m := range effective {
		if strings.Contains(m.Content, "Summary of earlier conversation") {
			foundSummaryMessage = true
		}
	}
	if !foundSummaryMessage {
		t.Error("expected a synthetic summary message in the effective history")
	}
	// The very first and very last original messages must survive
	// verbatim — head/tail are never themselves summarized.
	if effective[0].Content != history[0].Content {
		t.Error("first message should be preserved verbatim in the head")
	}
	if effective[len(effective)-1].Content != history[len(history)-1].Content {
		t.Error("last message should be preserved verbatim in the tail")
	}
}

func TestCompactHistoryOnlySummarizesNewMiddleOnSubsequentCalls(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		{Content: "first summary"},
		{Content: "updated summary"},
	}}
	a := New(client, nil)
	cfg := HistorySummaryConfig{HeadTokens: 100, TailTokens: 100, ThresholdTokens: 200}

	history := repeatedHistory(40, 50)
	_, summary1, throughIdx1, err := a.CompactHistory(context.Background(), history, "", 0, cfg)
	if err != nil {
		t.Fatalf("first CompactHistory: %v", err)
	}
	if summary1 != "first summary" {
		t.Fatalf("summary1 = %q", summary1)
	}

	// Conversation grows further — a second compaction should only feed
	// the model the delta plus the existing summary, not the whole
	// history from scratch again.
	grown := append(history, repeatedHistory(20, 50)...)
	_, summary2, throughIdx2, err := a.CompactHistory(context.Background(), grown, summary1, throughIdx1, cfg)
	if err != nil {
		t.Fatalf("second CompactHistory: %v", err)
	}
	if summary2 != "updated summary" {
		t.Errorf("summary2 = %q", summary2)
	}
	if throughIdx2 <= throughIdx1 {
		t.Errorf("throughIdx2 = %d, want greater than throughIdx1 = %d", throughIdx2, throughIdx1)
	}
	if client.calls != 2 {
		t.Errorf("expected exactly 2 summarization calls total, got %d", client.calls)
	}
	// The second call's prompt should reference the existing summary,
	// not silently ignore it.
	secondCallMessages := client.seen[1]
	if len(secondCallMessages) != 1 || !strings.Contains(secondCallMessages[0].Content, "first summary") {
		t.Error("second summarization call should have included the existing summary in its prompt")
	}
}

func TestEstimateTokensUsesRealPromptTokensAsBaseline(t *testing.T) {
	history := []HistoryMessage{
		{Role: "user", Content: strings.Repeat("x", 300)},
		{Role: "assistant", Content: "reply", PromptTokens: 5000},
		{Role: "user", Content: strings.Repeat("y", 30)}, // only this is estimated by char count
	}
	got := estimateTokens(history)
	want := 5000 + len(history[1].Content)/charsPerTokenEstimate + 30/charsPerTokenEstimate
	if got != want {
		t.Errorf("estimateTokens = %d, want %d", got, want)
	}
}
