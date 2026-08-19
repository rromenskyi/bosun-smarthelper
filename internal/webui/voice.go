package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/voice"
)

// maxAudioUploadBytes bounds a push-to-talk recording — generous for a
// spoken command (well under a minute even at a high bitrate), but still
// bounded.
const maxAudioUploadBytes = 10 << 20

// SetTTSEngine wires in text-to-speech (see docs/voice.md). Optional: nil
// (the default) means /api/tts reports the feature unavailable.
func (s *Server) SetTTSEngine(engine voice.TTSEngine) {
	s.ttsEngine = engine
}

// SetSTTEngine wires in speech-to-text (see docs/voice.md). Optional: nil
// (the default) means /api/stt reports the feature unavailable.
func (s *Server) SetSTTEngine(engine voice.STTEngine) {
	s.sttEngine = engine
}

// handleSTT accepts a browser recording (whatever MediaRecorder produced —
// typically audio/webm;codecs=opus) as multipart form field "audio",
// converts it to the 16kHz mono PCM WAV whisper.cpp expects, and returns
// the recognized text.
func (s *Server) handleSTT(w http.ResponseWriter, r *http.Request) {
	if s.sttEngine == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "speech-to-text is not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioUploadBytes)
	if err := r.ParseMultipartForm(maxAudioUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	file, _, err := r.FormFile("audio")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio file is required"})
		return
	}
	defer file.Close()
	rawAudio, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read audio"})
		return
	}

	wav, err := convertToWAV(r.Context(), rawAudio)
	if err != nil {
		s.logger.Error("stt audio conversion failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not process audio"})
		return
	}

	start := time.Now()
	transcript, err := s.sttEngine.Transcribe(r.Context(), wav)
	elapsed := time.Since(start)
	if err != nil {
		s.logger.Error("stt transcription failed", "error", err, "elapsed_ms", elapsed.Milliseconds())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transcription failed"})
		return
	}
	text := strings.TrimSpace(transcript.Text)
	s.logger.Info("stt transcription", "elapsed_ms", elapsed.Milliseconds(), "text_length", len(text))
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "language": transcript.Language})
}

// convertToWAV shells out to ffmpeg (same subprocess pattern as
// internal/webui/pdf.go's pdftoppm/tesseract calls) to turn whatever
// format the browser recorded into 16kHz mono PCM WAV, entirely via
// stdin/stdout pipes — no temp files, matching "don't persist raw audio."
func convertToWAV(ctx context.Context, input []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", "pipe:0",
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
		"-f", "wav", "pipe:1",
	)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type ttsRequest struct {
	Text string `json:"text"`
}

// handleTTS synthesizes speech for a chat message's text — used by the
// web UI's per-message "speak" button. Text is passed to the engine
// unmodified; punctuation and case are intonation cues, not noise to
// strip (see docs/voice.md).
func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	if s.ttsEngine == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "text-to-speech is not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request ttsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if request.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}

	start := time.Now()
	audio, err := s.ttsEngine.Synthesize(r.Context(), request.Text)
	elapsed := time.Since(start)
	if err != nil {
		s.logger.Error("tts synthesis failed", "error", err, "elapsed_ms", elapsed.Milliseconds())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "synthesis failed"})
		return
	}
	s.logger.Info("tts synthesis", "elapsed_ms", elapsed.Milliseconds(), "text_length", len(request.Text))

	w.Header().Set("Content-Type", "audio/wav")
	w.Write(audio)
}
