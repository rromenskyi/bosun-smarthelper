package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/agent"
)

// blockingStreamingAsker holds each call open until release is closed,
// closing started (once, on the first call) so a test can deterministically
// observe a request "in flight".
type blockingStreamingAsker struct {
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
}

func newBlockingStreamingAsker() *blockingStreamingAsker {
	return &blockingStreamingAsker{started: make(chan struct{}), release: make(chan struct{})}
}

func (a *blockingStreamingAsker) Ask(_ context.Context, _ string) (string, error) {
	return "done", nil
}

func (a *blockingStreamingAsker) AskWithHistoryStreaming(
	_ context.Context, _ string, _ []agent.HistoryMessage, _ string, onEvent func(agent.StepEvent),
) (string, error) {
	onEvent(agent.StepEvent{Type: "step_start"})
	a.startedOnce.Do(func() { close(a.started) })
	<-a.release
	return "done", nil
}

func postChat(server *Server, message, sessionID string) *httptest.ResponseRecorder {
	body := `{"message":"` + message + `","session_id":"` + sessionID + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func decodeNDJSONEvents(t *testing.T, body string) []streamEvent {
	t.Helper()
	var events []streamEvent
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode ndjson line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestServerChatQueuesLocallyAndReportsPosition(t *testing.T) {
	asker := newBlockingStreamingAsker()
	server := NewServer(asker, func() Status { return Status{Provider: "local", Online: false} }, 5*time.Second, "ru", nil)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- postChat(server, "hi", "session-one") }()

	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never started")
	}

	// Second request should be told immediately that it's queued, not left
	// waiting silently. It joins the queue synchronously inside handleChat
	// before ever calling the asker, so by the time postChat returns (after
	// the whole exchange completes below) its very first ndjson line must
	// already be the queued event — httptest.ResponseRecorder only exposes
	// the body once the handler returns, so there's no way to peek at it
	// mid-flight, but event order is preserved regardless of that buffering.
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { secondDone <- postChat(server, "hi again", "session-two") }()

	// Give the second request a moment to actually reach the queue join
	// (it's racing this goroutine's scheduling, not asker.release).
	time.Sleep(50 * time.Millisecond)
	close(asker.release)

	var firstResp, secondResp *httptest.ResponseRecorder
	for i := 0; i < 2; i++ {
		select {
		case firstResp = <-firstDone:
		case secondResp = <-secondDone:
		case <-time.After(2 * time.Second):
			t.Fatal("a request never finished after release")
		}
	}
	if firstResp.Code != http.StatusOK {
		t.Errorf("first request status = %d", firstResp.Code)
	}
	if secondResp.Code != http.StatusOK {
		t.Errorf("second request status = %d", secondResp.Code)
	}
	secondEvents := decodeNDJSONEvents(t, secondResp.Body.String())
	if len(secondEvents) == 0 || secondEvents[0].Type != "queued" || secondEvents[0].Position != 1 {
		t.Fatalf("second request's first event = %#v, want {type: queued, position: 1}", secondEvents)
	}
}

func TestServerChatOnlineBypassesQueueEntirely(t *testing.T) {
	asker := newBlockingStreamingAsker()
	server := NewServer(asker, func() Status { return Status{Provider: "remote", Online: true} }, 5*time.Second, "ru", nil)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- postChat(server, "hi", "session-one") }()

	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never started")
	}

	// A second concurrent request must not block behind the first at all
	// when the provider is remote — release immediately proves neither
	// request ever entered the local queue.
	close(asker.release)

	select {
	case resp := <-firstDone:
		if resp.Code != http.StatusOK {
			t.Errorf("first request status = %d", resp.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request never finished")
	}

	second := postChat(server, "hi again", "session-two")
	if second.Code != http.StatusOK {
		t.Errorf("second request status = %d", second.Code)
	}
	for _, event := range decodeNDJSONEvents(t, second.Body.String()) {
		if event.Type == "queued" {
			t.Error("an online request must never be queued")
		}
	}
}
