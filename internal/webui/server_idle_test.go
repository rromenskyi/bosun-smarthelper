package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blockingAsker holds a chat request open until release is closed, so a
// test can deterministically observe TryIdle's behavior while one is
// "in flight".
type blockingAsker struct {
	started chan struct{}
	release chan struct{}
}

func (a *blockingAsker) Ask(_ context.Context, _ string) (string, error) {
	close(a.started)
	<-a.release
	return "done", nil
}

func TestServerTryIdleSkipsWhileChatInFlight(t *testing.T) {
	asker := &blockingAsker{started: make(chan struct{}), release: make(chan struct{})}
	server := NewServer(asker, nil, 5*time.Second, "ru", nil)

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

	if server.TryIdle(func() { t.Error("TryIdle ran fn while a chat request was in flight") }) {
		t.Error("TryIdle = true, want false while busy")
	}

	close(asker.release)

	deadline := time.After(2 * time.Second)
	for {
		ran := false
		if server.TryIdle(func() { ran = true }) {
			if !ran {
				t.Fatal("TryIdle reported success without running fn")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("TryIdle never became available after the chat request finished")
		default:
		}
	}
}
