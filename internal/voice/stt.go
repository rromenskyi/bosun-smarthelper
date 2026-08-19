package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// Transcript is what an STTEngine returns for one recording.
type Transcript struct {
	Text     string
	Language string
}

// STTEngine transcribes a WAV recording to text.
type STTEngine interface {
	Transcribe(ctx context.Context, wav []byte) (Transcript, error)
}

// WhisperCppSTT is an HTTP client for whisper.cpp's own `whisper-server`
// (see deploy/whisper/Dockerfile) — a separate long-running process, not a
// per-request subprocess like PiperTTS, since whisper.cpp's model load
// time is significant enough to be worth keeping resident.
type WhisperCppSTT struct {
	BaseURL    string
	Language   string
	HTTPClient *http.Client
}

type whisperInferenceResponse struct {
	Text string `json:"text"`
}

// Transcribe posts wav as a multipart file to whisper-server's /inference
// endpoint (confirmed against a real build of this exact pinned commit —
// see docs/voice.md) and returns its recognized text.
func (w *WhisperCppSTT) Transcribe(ctx context.Context, wav []byte) (Transcript, error) {
	client := w.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return Transcript{}, fmt.Errorf("build STT request: %w", err)
	}
	if _, err := part.Write(wav); err != nil {
		return Transcript{}, fmt.Errorf("build STT request: %w", err)
	}
	if w.Language != "" {
		if err := writer.WriteField("language", w.Language); err != nil {
			return Transcript{}, fmt.Errorf("build STT request: %w", err)
		}
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return Transcript{}, fmt.Errorf("build STT request: %w", err)
	}
	if err := writer.Close(); err != nil {
		return Transcript{}, fmt.Errorf("build STT request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/inference", &body)
	if err != nil {
		return Transcript{}, fmt.Errorf("build STT request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return Transcript{}, fmt.Errorf("whisper-server request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Transcript{}, fmt.Errorf("whisper-server returned %d: %s", resp.StatusCode, respBody)
	}

	var parsed whisperInferenceResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Transcript{}, fmt.Errorf("decode whisper-server response: %w", err)
	}
	return Transcript{Text: parsed.Text, Language: w.Language}, nil
}
