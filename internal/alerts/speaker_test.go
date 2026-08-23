package alerts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTTS struct {
	lastText string
	wav      []byte
	err      error
}

func (f *fakeTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	f.lastText = text
	if f.err != nil {
		return nil, f.err
	}
	return f.wav, nil
}

// fakePlayer writes a small shell script that appends its stdin to
// $OUT_FILE, standing in for aplay so the test can inspect exactly what
// bytes would have reached the real player — without needing actual
// audio hardware. Appends (not overwrites) so a siren-then-speech test
// can tell the two sequential plays apart in the order they happened.
func fakePlayer(t *testing.T) (path string, outFile string) {
	t.Helper()
	dir := t.TempDir()
	outFile = filepath.Join(dir, "out.wav")
	scriptPath := filepath.Join(dir, "fake-player.sh")
	script := "#!/bin/sh\ncat >> \"$OUT_FILE\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake player script: %v", err)
	}
	t.Setenv("OUT_FILE", outFile)
	return scriptPath, outFile
}

func TestSpeakerNotifierSynthesizesAndPlays(t *testing.T) {
	playerPath, outFile := fakePlayer(t)
	tts := &fakeTTS{wav: []byte("fake-wav-bytes")}
	notifier := &SpeakerNotifier{TTS: tts, PlayerPath: playerPath, Language: "ru"}

	err := notifier.Notify(context.Background(), Alert{Title: "Диск почти полон", Body: "осталось 5%"})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !strings.Contains(tts.lastText, "Внимание!") || !strings.Contains(tts.lastText, "Диск почти полон") {
		t.Errorf("synthesized text = %q, missing expected content", tts.lastText)
	}
	played, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read what the fake player received: %v", err)
	}
	if string(played) != "fake-wav-bytes" {
		t.Errorf("played bytes = %q, want the synthesized WAV unchanged", played)
	}
}

func TestSpeakerNotifierEnglishPreamble(t *testing.T) {
	playerPath, _ := fakePlayer(t)
	tts := &fakeTTS{wav: []byte("x")}
	notifier := &SpeakerNotifier{TTS: tts, PlayerPath: playerPath, Language: "en"}

	if err := notifier.Notify(context.Background(), Alert{Title: "Disk almost full", Body: "5% left"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !strings.Contains(tts.lastText, "Attention!") {
		t.Errorf("synthesized text = %q, want the English preamble", tts.lastText)
	}
}

func TestSpeakerNotifierPropagatesSynthesizeError(t *testing.T) {
	playerPath, _ := fakePlayer(t)
	tts := &fakeTTS{err: context.DeadlineExceeded}
	notifier := &SpeakerNotifier{TTS: tts, PlayerPath: playerPath}
	if err := notifier.Notify(context.Background(), Alert{Title: "x"}); err == nil {
		t.Fatal("expected an error when synthesis fails")
	}
}

func TestSpeakerNotifierPlaysSirenBeforeSpeechWhenRequested(t *testing.T) {
	playerPath, outFile := fakePlayer(t)
	tts := &fakeTTS{wav: []byte("speech-bytes")}
	notifier := &SpeakerNotifier{TTS: tts, PlayerPath: playerPath}

	if err := notifier.Notify(context.Background(), Alert{Title: "x", PlaySiren: true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	played, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read what the fake player received: %v", err)
	}
	if !strings.HasPrefix(string(played), string(sirenWAV)) {
		t.Error("siren bytes weren't played first")
	}
	if !strings.HasSuffix(string(played), "speech-bytes") {
		t.Error("synthesized speech wasn't played after the siren")
	}
}

func TestSpeakerNotifierSkipsSirenWhenNotRequested(t *testing.T) {
	playerPath, outFile := fakePlayer(t)
	tts := &fakeTTS{wav: []byte("speech-bytes")}
	notifier := &SpeakerNotifier{TTS: tts, PlayerPath: playerPath}

	if err := notifier.Notify(context.Background(), Alert{Title: "x", PlaySiren: false}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	played, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read what the fake player received: %v", err)
	}
	if string(played) != "speech-bytes" {
		t.Errorf("played = %q, want just the speech (no siren)", played)
	}
}

func TestSpeakerNotifierPropagatesPlayerError(t *testing.T) {
	tts := &fakeTTS{wav: []byte("x")}
	notifier := &SpeakerNotifier{TTS: tts, PlayerPath: "/no/such/binary"}
	if err := notifier.Notify(context.Background(), Alert{Title: "x"}); err == nil {
		t.Fatal("expected an error when the player binary doesn't exist")
	}
}
