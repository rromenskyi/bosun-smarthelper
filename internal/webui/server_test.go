package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/agent"
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
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Error("Content-Security-Policy header is missing")
	}
}

type conversationFakeAsker struct {
	answers   []string
	histories [][]agent.HistoryMessage
}

func (f *conversationFakeAsker) Ask(_ context.Context, _ string) (string, error) {
	return f.answers[0], nil
}

func (f *conversationFakeAsker) AskWithHistory(_ context.Context, _ string, history []agent.HistoryMessage) (string, error) {
	f.histories = append(f.histories, append([]agent.HistoryMessage(nil), history...))
	answer := f.answers[len(f.histories)-1]
	return answer, nil
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
	if !strings.HasPrefix(asker.seen, "Отвечай по-русски.") {
		t.Errorf("agent prompt = %q", asker.seen)
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
