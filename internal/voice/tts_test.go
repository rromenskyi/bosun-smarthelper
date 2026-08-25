package voice

import (
	"context"
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
