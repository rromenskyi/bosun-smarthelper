// Package voice holds the (early, TTS-only so far) local voice interface
// — see docs/voice.md.
package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
)

// whitespaceRun matches any run of whitespace, including newlines —
// collapsing it to a single space is what stripMarkdownForSpeech's own
// client-side cleanup (index.html) does NOT do (it only collapses
// spaces/tabs), so this is the actual boundary that guarantees no raw
// newline ever reaches espeak. Needed because reply text can contain
// them legitimately (the adventure game's engine output hard-wraps
// lines the way 1977 terminal games did) and a literal '\n' fed
// straight into espeak's phonemizer produces an audible glitch — not a
// clean pause — confirmed by comparing synthesis of the same sentence
// with and without an embedded newline: the newline version has a
// spurious ~36000-magnitude sample jump (versus ~8-12000 for normal
// speech transients) exactly where the newline was, and even loses one
// of its two expected sentence-boundary silence gaps.
var whitespaceRun = regexp.MustCompile(`\s+`)

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
// to stdout. Punctuation and case are passed through unmodified — the
// LLM's intonation cues, not something to strip (see docs/voice.md) —
// but any run of whitespace, including newlines, collapses to a single
// space first (see whitespaceRun).
func (p *PiperTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(whitespaceRun.ReplaceAllString(text, " "))
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
	return fixWavHeaderSize(stdout.Bytes()), nil
}

// fixWavHeaderSize corrects the RIFF and "data" chunk size fields to the
// actual byte count. piper_exe writes a WAV to a stream it can't seek
// back on (stdout, possibly a pipe), so writeWavStreamHeader declares a
// large placeholder size upfront instead of the real one — but by the
// time this function runs, the whole file is already buffered in
// memory, so the real size is trivially known. Left uncorrected, this
// looks fine to `file`/most players (which just read until EOF), but a
// declared size wildly larger than the actual data confuses some
// decoders — worth ruling out as a source of playback artifacts before
// suspecting the audio samples themselves.
func fixWavHeaderSize(wav []byte) []byte {
	const riffHeaderSize = 8 // "RIFF" + 4-byte size field
	if len(wav) < riffHeaderSize+4 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return wav
	}
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-riffHeaderSize)) //nolint:gosec // WAV size fields are always 32-bit

	pos := 12
	for pos+8 <= len(wav) {
		chunkID := string(wav[pos : pos+4])
		chunkSize := binary.LittleEndian.Uint32(wav[pos+4 : pos+8])
		if chunkID == "data" {
			binary.LittleEndian.PutUint32(wav[pos+4:pos+8], uint32(len(wav)-(pos+8))) //nolint:gosec // WAV size fields are always 32-bit
			break
		}
		pos += 8 + int(chunkSize)
	}
	return wav
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
