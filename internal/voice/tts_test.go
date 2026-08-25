package voice

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// fakePiperScript stands in for piper_exe: it verifies the exact flags
// PiperTTS.Synthesize passes, then writes a fixed byte sequence to stdout
// so the test can assert Synthesize returns it unmodified. Doesn't
// require the real binary or a voice model to be present.
func fakePiperScript(t *testing.T, wantModel, wantEspeakData string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-piper.sh")
	content := `#!/bin/sh
if [ "$2" != "` + wantModel + `" ]; then echo "unexpected model: $2" >&2; exit 1; fi
if [ "$4" != "` + wantEspeakData + `" ]; then echo "unexpected espeak data: $4" >&2; exit 1; fi
if [ "$5" != "--output_file" ] || [ "$6" != "-" ]; then echo "unexpected output flag" >&2; exit 1; fi
cat > /dev/null
printf 'FAKEWAV'
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake piper script: %v", err)
	}
	return script
}

func TestPiperTTSSynthesize(t *testing.T) {
	binary := fakePiperScript(t, "/models/voice.onnx", "/espeak-data")
	engine := &PiperTTS{BinaryPath: binary, ModelPath: "/models/voice.onnx", EspeakDataPath: "/espeak-data"}

	audio, err := engine.Synthesize(context.Background(), "Капитан! Старпом на связи.")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio) != "FAKEWAV" {
		t.Errorf("audio = %q, want %q", audio, "FAKEWAV")
	}
}

// echoingPiperScript stands in for piper_exe but, unlike
// fakePiperScript, writes back whatever text it received on stdin
// (prefixed) instead of a fixed sequence — so a test can assert on the
// exact text Synthesize actually sent.
func echoingPiperScript(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "echoing-piper.sh")
	content := "#!/bin/sh\nprintf 'GOT:'\ncat\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write echoing piper script: %v", err)
	}
	return script
}

func TestPiperTTSSynthesizeCollapsesEmbeddedNewlines(t *testing.T) {
	// A literal newline fed straight into espeak's phonemizer produces an
	// audible glitch, not a clean pause (confirmed by direct comparison —
	// see the comment on whitespaceRun) — text with the adventure game's
	// own hard-wrapped lines must never reach piper_exe with newlines
	// still in it.
	script := echoingPiperScript(t)
	engine := &PiperTTS{BinaryPath: script, ModelPath: "m", EspeakDataPath: "e"}

	audio, err := engine.Synthesize(context.Background(), "An east\npassage ends here.\n\nRough stone steps lead down.")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	got := string(audio)
	want := "GOT:An east passage ends here. Rough stone steps lead down."
	if got != want {
		t.Errorf("text sent to piper_exe = %q, want %q", got, want)
	}
}

func TestPiperTTSSynthesizeRejectsEmptyText(t *testing.T) {
	engine := &PiperTTS{BinaryPath: "/does/not/matter", ModelPath: "m", EspeakDataPath: "e"}
	if _, err := engine.Synthesize(context.Background(), "   "); err == nil {
		t.Error("expected an error for empty text, got nil")
	}
}

func TestPiperTTSSynthesizePropagatesCommandFailure(t *testing.T) {
	script := filepath.Join(t.TempDir(), "failing-piper.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'boom' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing script: %v", err)
	}
	engine := &PiperTTS{BinaryPath: script, ModelPath: "m", EspeakDataPath: "e"}

	_, err := engine.Synthesize(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// recordingTTS is a stub TTSEngine that records the text it was asked to
// synthesize, so tests can assert which of two engines LanguageAwareTTS
// picked without needing real piper_exe scripts on each side.
type recordingTTS struct {
	gotText string
}

func (r *recordingTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	r.gotText = text
	return []byte("ok"), nil
}

func TestLanguageAwareTTSRoutesByScript(t *testing.T) {
	russian := &recordingTTS{}
	english := &recordingTTS{}
	engine := &LanguageAwareTTS{Russian: russian, English: english}

	if _, err := engine.Synthesize(context.Background(), "You are in a maze of twisty passages."); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if english.gotText == "" || russian.gotText != "" {
		t.Errorf("English-only text should route to English, got russian=%q english=%q", russian.gotText, english.gotText)
	}

	russian.gotText, english.gotText = "", ""
	if _, err := engine.Synthesize(context.Background(), "Капитан, курс проложен."); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if russian.gotText == "" || english.gotText != "" {
		t.Errorf("Cyrillic text should route to Russian, got russian=%q english=%q", russian.gotText, english.gotText)
	}

	russian.gotText, english.gotText = "", ""
	if _, err := engine.Synthesize(context.Background(), "There is a lamp здесь."); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if russian.gotText == "" {
		t.Error("mixed text with any Cyrillic should still route to Russian")
	}
}

// buildWav constructs a minimal 44-byte-header PCM WAV, with the RIFF
// and data chunk sizes set to declaredDataSize regardless of how much
// PCM data actually follows — mimicking piper_exe's own placeholder
// size (it can't seek back on its output stream to fix this up itself).
func buildWav(pcmData []byte, declaredDataSize uint32) []byte {
	wav := make([]byte, 0, 44+len(pcmData))
	wav = append(wav, "RIFF"...)
	riffSize := make([]byte, 4)
	binary.LittleEndian.PutUint32(riffSize, declaredDataSize+36)
	wav = append(wav, riffSize...)
	wav = append(wav, "WAVEfmt "...)
	wav = append(wav, 16, 0, 0, 0)      // fmt chunk size
	wav = append(wav, 1, 0)             // PCM
	wav = append(wav, 1, 0)             // mono
	wav = append(wav, 0x22, 0x56, 0, 0) // 22050 Hz
	wav = append(wav, 0x44, 0xac, 0, 0) // byte rate (placeholder)
	wav = append(wav, 2, 0)             // block align
	wav = append(wav, 16, 0)            // bits per sample
	wav = append(wav, "data"...)
	dataSize := make([]byte, 4)
	binary.LittleEndian.PutUint32(dataSize, declaredDataSize)
	wav = append(wav, dataSize...)
	wav = append(wav, pcmData...)
	return wav
}

func TestFixWavHeaderSizeCorrectsBogusPlaceholder(t *testing.T) {
	pcm := []byte{1, 2, 3, 4, 5, 6, 7, 8} // 4 fake int16 samples
	wav := buildWav(pcm, 2147479552)      // piper's real-world placeholder value

	fixed := fixWavHeaderSize(wav)

	gotRiffSize := binary.LittleEndian.Uint32(fixed[4:8])
	gotDataSize := binary.LittleEndian.Uint32(fixed[40:44])
	if wantRiff := uint32(len(fixed) - 8); gotRiffSize != wantRiff {
		t.Errorf("RIFF size = %d, want %d", gotRiffSize, wantRiff)
	}
	if wantData := uint32(len(pcm)); gotDataSize != wantData {
		t.Errorf("data chunk size = %d, want %d", gotDataSize, wantData)
	}
}

func TestFixWavHeaderSizeIgnoresNonWav(t *testing.T) {
	notWav := []byte("not a wav file at all")
	if got := fixWavHeaderSize(notWav); string(got) != string(notWav) {
		t.Error("non-WAV input should pass through unchanged")
	}
}

func TestLanguageAwareTTSFallsBackWithoutEnglish(t *testing.T) {
	russian := &recordingTTS{}
	engine := &LanguageAwareTTS{Russian: russian}

	if _, err := engine.Synthesize(context.Background(), "Pure English text."); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if russian.gotText == "" {
		t.Error("with no English engine configured, everything should still go to Russian")
	}
}
