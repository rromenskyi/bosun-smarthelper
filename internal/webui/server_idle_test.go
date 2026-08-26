package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/llm"
)

// blockingAsker holds a chat request open until release is closed, so a
// test can deterministically observe TryIdleAfter's behavior while one is
// "in flight".
type blockingAsker struct {
	started chan struct{}
	release chan struct{}
}

func (a *blockingAsker) Ask(_ context.Context, _ string) (string, llm.Usage, error) {
	close(a.started)
	<-a.release
	return "done", llm.Usage{}, nil
}

func startBlockingChat(t *testing.T, server *Server, asker *blockingAsker) {
	t.Helper()
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"message":"hi"}`))
		request.Header.Set("Content-Type", "application/json")
		server.Handler().ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-asker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("chat request never started")
	}
}

func TestServerTryIdleAfterSkipsWhileChatInFlight(t *testing.T) {
	asker := &blockingAsker{started: make(chan struct{}), release: make(chan struct{})}
	server := NewServer(asker, nil, 5*time.Second, "ru", nil)
	startBlockingChat(t, server, asker)

	if server.TryIdleAfter(0, func() { t.Error("TryIdleAfter ran fn while a chat request was in flight") }) {
		t.Error("TryIdleAfter = true, want false while busy")
	}

	close(asker.release)

	deadline := time.After(2 * time.Second)
	for {
		ran := false
		if server.TryIdleAfter(0, func() { ran = true }) {
			if !ran {
				t.Fatal("TryIdleAfter reported success without running fn")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("TryIdleAfter never became available after the chat request finished")
		default:
		}
	}
}

func TestServerTryIdleAfterWaitsOutQuietPeriod(t *testing.T) {
	asker := &blockingAsker{started: make(chan struct{}), release: make(chan struct{})}
	server := NewServer(asker, nil, 5*time.Second, "ru", nil)
	startBlockingChat(t, server, asker)
	close(asker.release)

	// Wait for the chat handler to actually release the slot. Its deferred
	// markChatActivity runs first (declared after the slot-release defer,
	// so LIFO puts it first), so by the time the slot is free the activity
	// timestamp is already recorded.
	deadline := time.After(2 * time.Second)
	for !server.TryIdleAfter(0, func() {}) {
		select {
		case <-deadline:
			t.Fatal("chat request never released the slot")
		default:
		}
	}

	if server.TryIdleAfter(time.Hour, func() { t.Error("must not run: quiet period has not elapsed") }) {
		t.Error("TryIdleAfter(time.Hour, ...) = true right after a chat finished, want false")
	}
	if !server.TryIdleAfter(0, func() {}) {
		t.Error("TryIdleAfter(0, ...) = false, want true — a zero quiet period should ignore last-activity timing")
	}
}
