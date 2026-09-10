package webui

import (
	"context"

	"github.com/roman220/bosun-smarthelper/internal/agent"
	"github.com/roman220/bosun-smarthelper/internal/tools"
)

// SessionHistory implements tools.HistoryProvider — search_history's one
// way to reach the full, original conversation a summary (see
// compactHistoryForTurn) was built from. Returns ok=false only for a
// session ID that doesn't exist at all; an existing session with no
// messages yet just returns an empty, ok=true slice.
func (s *Server) SessionHistory(sessionID string) ([]tools.HistoryEntry, bool) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	entries := make([]tools.HistoryEntry, len(session.History))
	for i, m := range session.History {
		entries[i] = tools.HistoryEntry{Role: m.Role, Content: m.Content}
	}
	return entries, true
}

// historyCompactor is implemented by *agent.Agent — narrowed to an
// interface (matching personaSetter/dynamicTopicsSetter's pattern) so a
// test double for Asker that doesn't implement it just skips compaction
// entirely rather than needing to fake this too.
type historyCompactor interface {
	CompactHistory(
		ctx context.Context,
		history []agent.HistoryMessage,
		existingSummary string,
		summarizedThroughIndex int,
		cfg agent.HistorySummaryConfig,
	) (effective []agent.HistoryMessage, newSummary string, newSummarizedThroughIndex int, err error)
}

// historySummaryConfig reads the three settings-page knobs (see
// settings.Data's HistorySummary* fields) — settingsStore nil (never
// configured) just means agent.CompactHistory's own built-in defaults
// apply, the zero value here.
func (s *Server) historySummaryConfig() agent.HistorySummaryConfig {
	if s.settingsStore == nil {
		return agent.HistorySummaryConfig{}
	}
	data := s.settingsStore.Get()
	return agent.HistorySummaryConfig{
		HeadTokens:      data.HistorySummaryHeadTokens,
		TailTokens:      data.HistorySummaryTailTokens,
		ThresholdTokens: data.HistorySummaryThresholdTokens,
	}
}

func (s *Server) historySummaryState(sessionID string) (summary string, summarizedThroughIndex int) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return "", 0
	}
	return session.HistorySummary, session.HistorySummarizedThroughIndex
}

func (s *Server) saveHistorySummaryState(sessionID, summary string, summarizedThroughIndex int) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	session.HistorySummary = summary
	session.HistorySummarizedThroughIndex = summarizedThroughIndex
	s.sessions[sessionID] = session
	s.persistLocked()
}

// compactHistoryForTurn returns the history to actually send to the LLM
// this turn — history unchanged if s.asker doesn't support compaction, or
// if agent.CompactHistory decides it's still under threshold (the common
// case). See docs/settings.md and agent.CompactHistory's own doc comment
// for why this exists: a long-running chat sending its full,
// ever-growing history on every turn has caused real, live failures
// (an 8192-token remote backend rejecting an oversized request outright,
// and separately just taking too long to process — especially on the
// CPU-bound local model).
//
// A compaction failure (e.g. the summarization LLM call itself erroring)
// degrades to sending history uncompacted rather than failing the turn —
// worse-case is the same failure mode this exists to prevent, not a new
// one, so it's logged and swallowed rather than surfaced to the user.
func (s *Server) compactHistoryForTurn(ctx context.Context, sessionID string, history []agent.HistoryMessage) []agent.HistoryMessage {
	compactor, ok := s.asker.(historyCompactor)
	if !ok {
		return history
	}
	existingSummary, summarizedThrough := s.historySummaryState(sessionID)
	effective, newSummary, newSummarizedThrough, err := compactor.CompactHistory(
		ctx, history, existingSummary, summarizedThrough, s.historySummaryConfig(),
	)
	if err != nil {
		s.logger.Warn("compact chat history", "session_id", sessionID, "error", err)
		return history
	}
	if newSummary != existingSummary || newSummarizedThrough != summarizedThrough {
		s.saveHistorySummaryState(sessionID, newSummary, newSummarizedThrough)
	}
	return effective
}
