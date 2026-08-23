package alerts

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os/exec"
)

// sirenWAV is a short, built-in two-tone alternating siren (~1.6s, 16kHz
// mono PCM) — baked into the binary via go:embed rather than a config
// path or upload UI, deliberately: one signal is enough to mean "pay
// attention," and embedding keeps this offline-first (no asset to
// separately ship/mount) the same way Piper's voice data is baked into
// the image rather than fetched at runtime.
//
//go:embed assets/siren.wav
var sirenWAV []byte

// ttsEngine matches voice.TTSEngine's shape without importing internal/voice
// directly — alerts and voice are independent features that happen to
// share this one capability; a Piper TTS instance already wired up for
// spoken chat replies satisfies this with zero extra setup.
type ttsEngine interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// SpeakerNotifier speaks an alert out loud through the host's own audio
// output — the one channel that reaches someone without a phone in hand,
// e.g. asleep belowdecks. Synthesizes through the same Piper engine
// regular voice replies use, then plays the WAV by shelling out to
// PlayerPath (aplay by default) — see docs/alerts.md for the container's
// /dev/snd passthrough this needs to actually reach real hardware.
type SpeakerNotifier struct {
	TTS ttsEngine
	// PlayerPath is the WAV player to shell out to; empty defaults to
	// "aplay" (ALSA, reads a WAV from stdin with "-").
	PlayerPath string
	// Language picks the spoken preamble ("Attention!"/"Внимание!") —
	// "en" or "ru" (default), matching the web UI's own two-language
	// convention. The alert's own Title/Body are spoken as-is regardless.
	Language string
}

func (s *SpeakerNotifier) Notify(ctx context.Context, alert Alert) error {
	playerPath := s.PlayerPath
	if playerPath == "" {
		playerPath = "aplay"
	}

	if alert.PlaySiren {
		if err := s.play(ctx, playerPath, sirenWAV); err != nil {
			return fmt.Errorf("play alert siren: %w", err)
		}
	}

	preamble := "Внимание!"
	if s.Language == "en" {
		preamble = "Attention!"
	}
	text := fmt.Sprintf("%s %s. %s", preamble, alert.Title, alert.Body)
	wav, err := s.TTS.Synthesize(ctx, text)
	if err != nil {
		return fmt.Errorf("synthesize alert speech: %w", err)
	}
	if err := s.play(ctx, playerPath, wav); err != nil {
		return fmt.Errorf("play alert audio: %w", err)
	}
	return nil
}

func (s *SpeakerNotifier) play(ctx context.Context, playerPath string, wav []byte) error {
	cmd := exec.CommandContext(ctx, playerPath, "-")
	cmd.Stdin = bytes.NewReader(wav)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}
