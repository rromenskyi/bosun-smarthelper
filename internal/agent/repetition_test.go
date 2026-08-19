package agent

import (
	"strings"
	"testing"
)

func TestRepetitionDetectorCatchesRunawayToken(t *testing.T) {
	var d repetitionDetector
	triggered := false
	for i := 0; i < 20; i++ {
		if d.feed("<pad>") {
			triggered = true
			break
		}
	}
	if !triggered {
		t.Error("expected the detector to fire on 20 repeats of <pad>")
	}
}

func TestRepetitionDetectorIgnoresNormalProse(t *testing.T) {
	var d repetitionDetector
	text := "Капитан! Старпом на связи. Системы в норме, курс держим. " +
		"Процессор загружен на 5%, память — 60%, диск — 44%. Всё идёт ровно, без шторма!"
	for _, word := range strings.Fields(text) {
		if d.feed(word + " ") {
			t.Fatalf("false positive on ordinary prose at word %q", word)
		}
	}
}

func TestRepetitionDetectorIgnoresShortLegitimateRepeats(t *testing.T) {
	var d repetitionDetector
	// A handful of repeated exclamation marks or "ha"s is normal, not a
	// degenerate loop — only repetitionMinRepeats+ consecutive copies count.
	if d.feed(strings.Repeat("!", repetitionMinRepeats-1)) {
		t.Error("should not trigger on fewer than the minimum repeat count")
	}
}
