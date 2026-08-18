package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/agent"
	"github.com/roman220/ai-local-smarthelper/internal/llm"
)

type fakeAsker struct {
	answer string
	err    error
	seen   string
}

func (f *fakeAsker) Ask(_ context.Context, message string) (string, error) {
	f.seen = message
	return f.answer, f.err
}

func TestServerIndex(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Bosun") {
		t.Error("embedded UI is missing its title")
	}
	csp := response.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header is missing")
	}
	// The bell chime is an embedded data: URI <audio> source. media-src falls
	// back to default-src 'self' when unset, which blocks data: — silently,
	// since playback errors are swallowed client-side. Regression test for
	// that exact bug.
	if !strings.Contains(csp, "media-src") || !strings.Contains(csp, "data:") {
		t.Errorf("CSP = %q, want an explicit media-src allowing data: for the bell chime", csp)
	}
}

type conversationFakeAsker struct {
	answers   []string
	histories [][]agent.HistoryMessage
	messages  []string
	languages []string
}

func (f *conversationFakeAsker) Ask(_ context.Context, _ string) (string, error) {
	return f.answers[0], nil
}

func (f *conversationFakeAsker) AskWithHistory(_ context.Context, message string, history []agent.HistoryMessage, language string) (string, error) {
	f.histories = append(f.histories, append([]agent.HistoryMessage(nil), history...))
	f.messages = append(f.messages, message)
	f.languages = append(f.languages, language)
	answer := f.answers[len(f.histories)-1]
	return answer, nil
}

// streamingFakeAsker implements streamingConversationAsker, replaying a
// scripted sequence of agent.StepEvent for each call.
type streamingFakeAsker struct {
	steps  [][]agent.StepEvent
	answer string
	calls  int
}

func (f *streamingFakeAsker) Ask(_ context.Context, _ string) (string, error) {
	return f.answer, nil
}

func (f *streamingFakeAsker) AskWithHistoryStreaming(
	_ context.Context,
	_ string,
	_ []agent.HistoryMessage,
	_ string,
	onEvent func(agent.StepEvent),
) (string, error) {
	for _, event := range f.steps[f.calls] {
		onEvent(event)
	}
	f.calls++
	return f.answer, nil
}

func TestServerChatStreamingWritesNDJSONEvents(t *testing.T) {
	asker := &streamingFakeAsker{
		answer: "Сейчас 22.5°C.",
		steps: [][]agent.StepEvent{{
			{Type: "step_start"},
			{Type: "delta", Delta: llm.StreamDelta{Kind: "fold", Text: "→ {\"temperature_c\":22.5}"}},
			{Type: "step_start"},
			{Type: "delta", Delta: llm.StreamDelta{Kind: "prose", Text: "Сейчас "}},
			{Type: "delta", Delta: llm.StreamDelta{Kind: "prose", Text: "22.5°C."}},
		}},
	}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"погода?","session_id":"stream-test-1"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if ct := response.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", ct)
	}

	decoder := json.NewDecoder(response.Body)
	var events []streamEvent
	for {
		var event streamEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		events = append(events, event)
	}

	wantTypes := []string{"step_start", "delta", "step_start", "delta", "delta", "done"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v, want %d events of types %v", events, len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("event %d type = %q, want %q", i, events[i].Type, want)
		}
	}
	if events[1].Kind != "fold" {
		t.Errorf("event 1 kind = %q, want fold", events[1].Kind)
	}
	if events[5].SessionID != "stream-test-1" {
		t.Errorf("done event session_id = %q", events[5].SessionID)
	}

	// The session store must hold the real final answer regardless of how
	// it was streamed in pieces.
	history := server.loadHistory("stream-test-1")
	if len(history) != 2 || history[1].Content != "Сейчас 22.5°C." {
		t.Errorf("saved history = %#v", history)
	}
}

func TestServerChatStreamingErrorEvent(t *testing.T) {
	asker := &streamingErrorAsker{}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the stream already started", response.Code)
	}

	decoder := json.NewDecoder(response.Body)
	var last streamEvent
	for {
		var event streamEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		last = event
	}
	if last.Type != "error" {
		t.Errorf("last event type = %q, want error", last.Type)
	}
}

type streamingErrorAsker struct{}

func (streamingErrorAsker) Ask(_ context.Context, _ string) (string, error) {
	return "", errors.New("boom")
}

func (streamingErrorAsker) AskWithHistoryStreaming(
	_ context.Context,
	_ string,
	_ []agent.HistoryMessage,
	_ string,
	onEvent func(agent.StepEvent),
) (string, error) {
	onEvent(agent.StepEvent{Type: "step_start"})
	return "", errors.New("boom")
}

func TestServerChatSessionHistoryAndClear(t *testing.T) {
	asker := &conversationFakeAsker{answers: []string{"Приятно познакомиться.", "Тебя зовут Рома.", "Истории нет."}}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	handler := server.Handler()
	sessionID := "test-session-123"

	postChat := func(message string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"message":%q,"language":"ru","session_id":%q}`, message, sessionID)
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := postChat("Меня зовут Рома"); response.Code != http.StatusOK {
		t.Fatalf("first chat status = %d: %s", response.Code, response.Body.String())
	}
	if response := postChat("Как меня зовут?"); response.Code != http.StatusOK {
		t.Fatalf("second chat status = %d: %s", response.Code, response.Body.String())
	}
	if len(asker.histories[1]) != 2 || asker.histories[1][0].Content != "Меня зовут Рома" {
		t.Errorf("second-turn history = %#v", asker.histories[1])
	}

	clearRequest := httptest.NewRequest(http.MethodPost, "/api/session/clear", strings.NewReader(`{"session_id":"test-session-123"}`))
	clearResponse := httptest.NewRecorder()
	handler.ServeHTTP(clearResponse, clearRequest)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("clear status = %d", clearResponse.Code)
	}
	postChat("Что было раньше?")
	if len(asker.histories[2]) != 0 {
		t.Errorf("history after clear = %#v, want empty", asker.histories[2])
	}
}

func TestServerChatHistoryLocalVsRemoteBudget(t *testing.T) {
	online := false
	asker := &conversationFakeAsker{answers: []string{"a1", "a2", "a3", "a4"}}
	// Provider must track online — handleChat gates on Provider (which
	// accounts for llm.router.prefer_remote), not Online, since the two can
	// disagree (see docs/streaming.md).
	status := func() Status {
		provider := "local"
		if online {
			provider = "remote"
		}
		return Status{Online: online, Provider: provider}
	}
	server := NewServer(asker, status, time.Second, "ru", nil, SessionOptions{
		Local:       HistoryBudget{Turns: 1, MaxChars: 4000},
		Remote:      HistoryBudget{Turns: 10, MaxChars: 40000},
		TTL:         time.Hour,
		MaxSessions: 10,
	})
	handler := server.Handler()
	sessionID := "history-budget-test"

	postChat := func(message string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"message":%q,"session_id":%q}`, message, sessionID)
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	// Three turns while offline (local model serving): the outgoing request
	// is trimmed to the small Local budget (1 turn = 2 messages) even once
	// more than that is actually stored.
	for i, msg := range []string{"turn one", "turn two", "turn three"} {
		if response := postChat(msg); response.Code != http.StatusOK {
			t.Fatalf("offline turn %d status = %d: %s", i, response.Code, response.Body.String())
		}
	}
	if got := len(asker.histories[0]); got != 0 {
		t.Errorf("first turn history len = %d, want 0 (nothing stored yet)", got)
	}
	if got := len(asker.histories[2]); got != 2 {
		t.Errorf("third turn (offline) history len = %d, want 2 (trimmed to Local budget, not the 4 actually stored)", got)
	}

	// Flip online: the next turn must see the FULL stored history (bounded
	// by the larger Remote budget), proving the earlier local-only trims
	// never discarded anything from storage.
	online = true
	if response := postChat("turn four"); response.Code != http.StatusOK {
		t.Fatalf("online turn status = %d: %s", response.Code, response.Body.String())
	}
	if got := len(asker.histories[3]); got != 6 {
		t.Errorf("online turn history len = %d, want 6 (all 3 prior turns preserved)", got)
	}
}

func TestServerChatLanguagePassedSeparatelyFromMessage(t *testing.T) {
	asker := &conversationFakeAsker{answers: []string{"Сейчас 22,5°C.", "Now 72°F."}}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	handler := server.Handler()

	post := func(message, language string) {
		body := fmt.Sprintf(`{"message":%q,"language":%q}`, message, language)
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}

	post("Какая погода?", "ru")
	post("What's the weather?", "en")

	if asker.messages[0] != "Какая погода?" || asker.languages[0] != "ru" {
		t.Errorf("call 0: message=%q language=%q, want unmodified message and language=ru", asker.messages[0], asker.languages[0])
	}
	if asker.messages[1] != "What's the weather?" || asker.languages[1] != "en" {
		t.Errorf("call 1: message=%q language=%q, want unmodified message and language=en", asker.messages[1], asker.languages[1])
	}
}

func TestServerSessionPersistsAcrossRestart(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	options := SessionOptions{TTL: time.Hour, MaxSessions: 10, StorePath: storePath}

	first := &conversationFakeAsker{answers: []string{"Приятно познакомиться."}}
	server1 := NewServer(first, nil, time.Second, "ru", nil, options)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"Меня зовут Рома","session_id":"restart-test-session"}`))
	response := httptest.NewRecorder()
	server1.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	// Simulate a service restart: a fresh Server pointed at the same store path.
	second := &conversationFakeAsker{answers: []string{"Тебя зовут Рома."}}
	server2 := NewServer(second, nil, time.Second, "ru", nil, options)
	history := server2.loadHistory("restart-test-session")
	if len(history) != 2 || history[0].Content != "Меня зовут Рома" {
		t.Fatalf("history after restart = %#v, want the turn saved before restart", history)
	}
}

func TestServerHistoryEndpointHydratesTranscript(t *testing.T) {
	asker := &conversationFakeAsker{answers: []string{"Привет!"}}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	handler := server.Handler()

	chatRequest := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"Привет","session_id":"history-endpoint-test"}`))
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chatRequest)
	if chatResponse.Code != http.StatusOK {
		t.Fatalf("chat status = %d", chatResponse.Code)
	}

	historyRequest := httptest.NewRequest(http.MethodGet, "/api/history?session_id=history-endpoint-test", nil)
	historyResponse := httptest.NewRecorder()
	handler.ServeHTTP(historyResponse, historyRequest)
	if historyResponse.Code != http.StatusOK {
		t.Fatalf("history status = %d", historyResponse.Code)
	}

	var payload struct {
		Messages []agent.HistoryMessage `json:"messages"`
	}
	if err := json.NewDecoder(historyResponse.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "Привет" {
		t.Errorf("messages = %#v, want the stored turn", payload.Messages)
	}

	// An unknown session_id hydrates to an empty (not null) list.
	unknownRequest := httptest.NewRequest(http.MethodGet, "/api/history?session_id=does-not-exist-12345678", nil)
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknownRequest)
	var emptyPayload struct {
		Messages []agent.HistoryMessage `json:"messages"`
	}
	if err := json.NewDecoder(unknownResponse.Body).Decode(&emptyPayload); err != nil {
		t.Fatalf("decode empty history response: %v", err)
	}
	if emptyPayload.Messages == nil || len(emptyPayload.Messages) != 0 {
		t.Errorf("messages = %#v, want an empty non-nil list", emptyPayload.Messages)
	}
}

func TestServerSessionClearRemovesPersistedSession(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	options := SessionOptions{TTL: time.Hour, MaxSessions: 10, StorePath: storePath}
	asker := &conversationFakeAsker{answers: []string{"Ответ."}}
	server := NewServer(asker, nil, time.Second, "ru", nil, options)
	handler := server.Handler()

	chatRequest := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"Привет","session_id":"clear-persist-test"}`))
	handler.ServeHTTP(httptest.NewRecorder(), chatRequest)

	clearRequest := httptest.NewRequest(http.MethodPost, "/api/session/clear", strings.NewReader(`{"session_id":"clear-persist-test"}`))
	handler.ServeHTTP(httptest.NewRecorder(), clearRequest)

	// A fresh server reading the same store must not see the cleared session.
	reloaded := NewServer(&conversationFakeAsker{answers: []string{"unused"}}, nil, time.Second, "ru", nil, options)
	if history := reloaded.loadHistory("clear-persist-test"); len(history) != 0 {
		t.Errorf("history after clear+restart = %#v, want empty", history)
	}
}

func TestServerChat(t *testing.T) {
	asker := &fakeAsker{answer: "Сейчас 22,5°C."}
	server := NewServer(asker, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"Какая погода?","language":"ru"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if asker.seen != "Какая погода?" {
		t.Errorf("agent message = %q, want the raw user message with no injected language prefix", asker.seen)
	}
	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Answer != asker.answer {
		t.Errorf("answer = %q", payload.Answer)
	}
}

func TestServerChatValidation(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: `{"message":""}`},
		{name: "unknown field", body: `{"message":"hi","extra":true}`},
		{name: "language", body: `{"message":"hi","language":"de"}`},
		{name: "trailing JSON", body: `{"message":"hi"}{"message":"again"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Errorf("status = %d", response.Code)
			}
		})
	}
}

func TestServerChatFailure(t *testing.T) {
	server := NewServer(&fakeAsker{err: errors.New("provider failed")}, nil, time.Second, "en", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hello"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "provider failed") {
		t.Error("internal provider error leaked to client")
	}
}

func TestServerStatus(t *testing.T) {
	server := NewServer(&fakeAsker{}, func() Status {
		return Status{Online: true, Provider: "remote"}
	}, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"provider":"remote"`) {
		t.Fatalf("unexpected status response: %d %s", response.Code, response.Body.String())
	}
}

func TestValidateBind(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "10.0.0.111:8080", "[::1]:8080"} {
		if err := ValidateBind(address); err != nil {
			t.Errorf("ValidateBind(%q): %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", ":8080", "8.8.8.8:8080", "example.com:8080"} {
		if err := ValidateBind(address); err == nil {
			t.Errorf("ValidateBind(%q) unexpectedly succeeded", address)
		}
	}
}
