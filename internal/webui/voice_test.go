package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeTTSEngine struct {
	audio []byte
	err   error
	text  string
}

func (f *fakeTTSEngine) Synthesize(_ context.Context, text string) ([]byte, error) {
	f.text = text
	if f.err != nil {
		return nil, f.err
	}
	return f.audio, nil
}

func TestServerTTSDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader(`{"text":"hi"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", response.Code)
	}
}

func TestServerTTSSynthesizesAndReturnsWav(t *testing.T) {
	engine := &fakeTTSEngine{audio: []byte("FAKEWAV")}
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetTTSEngine(engine)

	request := httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader(`{"text":"Капитан! Старпом на связи."}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Body.String() != "FAKEWAV" {
		t.Errorf("body = %q, want %q", response.Body.String(), "FAKEWAV")
	}
	if got := response.Header().Get("Content-Type"); got != "audio/wav" {
		t.Errorf("Content-Type = %q, want audio/wav", got)
	}
	if engine.text != "Капитан! Старпом на связи." {
		t.Errorf("text passed to engine = %q, want unmodified original", engine.text)
	}
}

func TestServerTTSRejectsEmptyText(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetTTSEngine(&fakeTTSEngine{audio: []byte("x")})

	request := httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader(`{"text":""}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func TestServerTTSSynthesisFailure(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetTTSEngine(&fakeTTSEngine{err: errors.New("boom")})

	request := httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader(`{"text":"hi"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", response.Code)
	}
}
