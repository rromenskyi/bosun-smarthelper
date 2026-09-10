package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/agent"
)

// compactingFakeAsker implements conversationAsker + historyCompactor —
// records every CompactHistory call it receives and, if scripted,
// replaces history with a fixed "compacted" stand-in before AskWithHistory
// ever sees it.
type compactingFakeAsker struct {
	conversationFakeAsker
	compactCalls []struct {
		history         []agent.HistoryMessage
		existingSummary string
		throughIndex    int
	}
	compactedHistory     []agent.HistoryMessage
	returnedSummary      string
	returnedThroughIndex int
	compactErr           error
}

func (f *compactingFakeAsker) CompactHistory(
	_ context.Context,
	history []agent.HistoryMessage,
	existingSummary string,
	summarizedThroughIndex int,
	_ agent.HistorySummaryConfig,
) ([]agent.HistoryMessage, string, int, error) {
	f.compactCalls = append(f.compactCalls, struct {
		history         []agent.HistoryMessage
		existingSummary string
		throughIndex    int
	}{append([]agent.HistoryMessage(nil), history...), existingSummary, summarizedThroughIndex})
	if f.compactErr != nil {
		return nil, "", 0, f.compactErr
	}
	if f.compactedHistory != nil {
		return f.compactedHistory, f.returnedSummary, f.returnedThroughIndex, nil
	}
	return history, existingSummary, summarizedThroughIndex, nil
}

func TestServerChatUsesCompactedHistoryAndPersistsSummaryState(t *testing.T) {
	asker := &compactingFakeAsker{
		conversationFakeAsker: conversationFakeAsker{answers: []string{"a1", "a2"}},
		compactedHistory:      []agent.HistoryMessage{{Role: "assistant", Content: "[summary stand-in]"}},
		returnedSummary:       "summary v1",
		returnedThroughIndex:  4,
	}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	handler := server.Handler()
	sessionID := "compaction-test"

	postChat := func(message string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"message":%q,"session_id":%q}`, message, sessionID)
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	if response := postChat("first"); response.Code != http.StatusOK {
		t.Fatalf("first turn status = %d: %s", response.Code, response.Body.String())
	}
	if len(asker.compactCalls) != 1 {
		t.Fatalf("expected 1 CompactHistory call, got %d", len(asker.compactCalls))
	}
	if asker.compactCalls[0].existingSummary != "" || asker.compactCalls[0].throughIndex != 0 {
		t.Errorf("first call state = %+v, want empty (nothing persisted yet)", asker.compactCalls[0])
	}
	// AskWithHistory must have received the compactor's output, not the
	// raw (empty, on this first turn) session history.
	if len(asker.histories[0]) != 1 || asker.histories[0][0].Content != "[summary stand-in]" {
		t.Errorf("AskWithHistory saw %+v, want the compacted stand-in", asker.histories[0])
	}

	if response := postChat("second"); response.Code != http.StatusOK {
		t.Fatalf("second turn status = %d: %s", response.Code, response.Body.String())
	}
	if len(asker.compactCalls) != 2 {
		t.Fatalf("expected 2 CompactHistory calls, got %d", len(asker.compactCalls))
	}
	// The second call must see what the first call returned — proving the
	// summary/index round-tripped through session persistence rather than
	// resetting every turn.
	second := asker.compactCalls[1]
	if second.existingSummary != "summary v1" || second.throughIndex != 4 {
		t.Errorf("second call state = %+v, want the first call's persisted result", second)
	}
}

func TestServerChatCompactionErrorFallsBackToUncompactedHistory(t *testing.T) {
	asker := &compactingFakeAsker{
		conversationFakeAsker: conversationFakeAsker{answers: []string{"a1", "a2"}},
		compactErr:            fmt.Errorf("summarization LLM call failed"),
	}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	handler := server.Handler()
	sessionID := "compaction-error-test"

	post := func(message string) int {
		body := fmt.Sprintf(`{"message":%q,"session_id":%q}`, message, sessionID)
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}

	if code := post("first"); code != http.StatusOK {
		t.Fatalf("first turn status = %d, want 200 even though compaction failed", code)
	}
	if code := post("second"); code != http.StatusOK {
		t.Fatalf("second turn status = %d", code)
	}
	// Second call's history came from actual session storage (the first
	// turn's real user+assistant messages), not the compactor's (never
	// returned, since it errored) output — confirms the fallback used the
	// real, uncompacted history rather than something empty or stale.
	if len(asker.histories[1]) != 2 {
		t.Errorf("second turn history len = %d, want 2 (first turn's real messages, uncompacted)", len(asker.histories[1]))
	}
}
