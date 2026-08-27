package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

// RemoteSTT is an HTTP client for an OpenAI-compatible
// /audio/transcriptions endpoint — e.g. Groq's hosted Whisper API,
// reached through this deployment's own reverse proxy rather than
// directly (same indirection as llm.RemoteClient). Local whisper.cpp on
// weak/no-AVX2 CPU hardware forces an unworkable choice between a fast,
// barely-usable transcription (tiny/base) and an accurate but
// multi-minute one (large-v3-turbo) — confirmed by direct A/B testing,
// not assumption. A remote GPU-backed Whisper sidesteps that entirely.
//
// Same multipart-upload shape as WhisperCppSTT, kept as a separate type
// rather than a shared base: the real differences (a different path,
// authentication, and a required "model" field since a remote endpoint
// can serve more than one model) are the whole point of having two.
type RemoteSTT struct {
	BaseURL    string
	Model      string
	APIKey     string
	Language   string
	HTTPClient *http.Client
}

// Transcribe posts wav to <BaseURL>/audio/transcriptions, OpenAI's
// standard Whisper endpoint shape (also what Groq's hosted API and a
// reverse proxy fronting it are expected to implement).
func (w *RemoteSTT) Transcribe(ctx context.Context, wav []byte) (Transcript, error) {
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
	if w.Model != "" {
		if err := writer.WriteField("model", w.Model); err != nil {
			return Transcript{}, fmt.Errorf("build STT request: %w", err)
		}
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/audio/transcriptions", &body)
	if err != nil {
		return Transcript{}, fmt.Errorf("build STT request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if w.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Transcript{}, fmt.Errorf("remote STT request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Transcript{}, fmt.Errorf("remote STT returned %d: %s", resp.StatusCode, respBody)
	}

	var parsed whisperInferenceResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Transcript{}, fmt.Errorf("decode remote STT response: %w", err)
	}
	return Transcript{Text: parsed.Text, Language: w.Language}, nil
}

// RoutedSTT prefers Remote while online, falling back to Local on any
// failure (including a NetworkAvailable-reported offline state) — same
// "prefer remote, degrade gracefully, one shared connectivity check"
// shape as llm.Router's chat provider selection, deliberately not a
// separate manual setting: direct A/B testing found no local model on
// this class of CPU worth switching *to*, so the real choice is just
// "is there a network to reach the good one right now."
//
// Unlike llm.Router's chat fallback (silent by design — a partial
// streamed answer can't be un-shown), a failed transcription has shown
// the user nothing yet, so a fallback here is logged: past experience
// this session with the chat router's own silent fallback made "why did
// this answer come from local" needlessly hard to diagnose after the
// fact, and there's no reason to repeat that here.
type RoutedSTT struct {
	Remote           STTEngine
	Local            STTEngine
	NetworkAvailable func(context.Context) bool
	Logger           *slog.Logger
}

func (r *RoutedSTT) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *RoutedSTT) Transcribe(ctx context.Context, wav []byte) (Transcript, error) {
	if r.Remote == nil {
		return r.Local.Transcribe(ctx, wav)
	}
	online := r.NetworkAvailable == nil || r.NetworkAvailable(ctx)
	if online {
		transcript, err := r.Remote.Transcribe(ctx, wav)
		if err == nil {
			return transcript, nil
		}
		if r.Local == nil {
			return Transcript{}, err
		}
		r.logger().Warn("remote STT failed; falling back to local", "error", err)
	} else if r.Local == nil {
		return Transcript{}, fmt.Errorf("remote STT unavailable (offline) and no local STT configured")
	}
	return r.Local.Transcribe(ctx, wav)
}
