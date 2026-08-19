package webui

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/voice"
)

type fakeSTTEngine struct {
	transcript voice.Transcript
	err        error
	gotWAV     []byte
}

func (f *fakeSTTEngine) Transcribe(_ context.Context, wav []byte) (voice.Transcript, error) {
	f.gotWAV = wav
	if f.err != nil {
		return voice.Transcript{}, f.err
	}
	return f.transcript, nil
}

func requireFfmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed; skipping STT audio-conversion test")
	}
}

func multipartAudioRequest(t *testing.T, audio []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", "recording.wav")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/stt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// minimalWAV is a tiny valid 16-bit PCM WAV (a few silent samples) —
// enough for ffmpeg to accept as real input.
var minimalWAV = []byte{
	'R', 'I', 'F', 'F', 36, 0, 0, 0, 'W', 'A', 'V', 'E',
	'f', 'm', 't', ' ', 16, 0, 0, 0, 1, 0, 1, 0,
	0x80, 0x3E, 0, 0, 0, 0x7D, 0, 0, 2, 0, 16, 0,
	'd', 'a', 't', 'a', 4, 0, 0, 0, 0, 0, 0, 0,
}

func TestServerSTTDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	request := multipartAudioRequest(t, minimalWAV)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", response.Code)
	}
}

func TestServerSTTTranscribes(t *testing.T) {
	requireFfmpeg(t)
	engine := &fakeSTTEngine{transcript: voice.Transcript{Text: " Капитан, курс на восток.\n", Language: "ru"}}
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSTTEngine(engine)

	request := multipartAudioRequest(t, minimalWAV)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(engine.gotWAV) == 0 {
		t.Error("engine did not receive converted WAV bytes")
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("Капитан")) {
		t.Errorf("body = %s, want it to contain the transcript", response.Body.String())
	}
}

func TestServerSTTMissingAudioField(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSTTEngine(&fakeSTTEngine{})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/stt", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func TestServerSTTTranscriptionFailure(t *testing.T) {
	requireFfmpeg(t)
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetSTTEngine(&fakeSTTEngine{err: errors.New("boom")})

	request := multipartAudioRequest(t, minimalWAV)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", response.Code)
	}
}
