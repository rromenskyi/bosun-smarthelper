// Package voice holds the (early, TTS-only so far) local voice interface
// — see docs/voice.md.
package voice

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"unicode"
)

// TTSEngine synthesizes text to speech, returning WAV bytes.
type TTSEngine interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// PiperTTS shells out to a built `piper_exe` (patched to emit 16-bit PCM
// WAV directly — see deploy/piper/wav-pcm16.patch) for each request.
// Piper's own model load is fast enough that a persistent server isn't
// needed — see docs/voice.md.
type PiperTTS struct {
	BinaryPath     string
	ModelPath      string
	EspeakDataPath string
}

// Synthesize pipes text to piper_exe's stdin and reads the WAV it writes
// to stdout. Text is passed through unmodified — punctuation and case are
// the LLM's intonation cues, not something to strip (see docs/voice.md).
func (p *PiperTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	cmd := exec.CommandContext(ctx, p.BinaryPath,
		"--model", p.ModelPath,
		"--espeak_data", p.EspeakDataPath,
		"--output_file", "-",
	)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("piper_exe: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// LanguageAwareTTS picks between two underlying voices per request —
// Russian has no way to read English well (or vice versa), and this
// avoids ever needing an LLM call just to decide which one to use. The
// heuristic is a plain character check, not detection of "the" language
// of the text: any Cyrillic at all routes to Russian, so a mixed
// sentence still gets the Russian voice (better at mangling a stray
// English word than an English voice mangling Russian ones).
type LanguageAwareTTS struct {
	Russian TTSEngine
	// English is optional — if nil, every request just uses Russian,
	// unchanged from before this type existed.
	English TTSEngine
}

func (m *LanguageAwareTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if m.English != nil && !hasCyrillic(text) {
		return m.English.Synthesize(ctx, text)
	}
	return m.Russian.Synthesize(ctx, text)
}

func hasCyrillic(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
