package documents

import (
	"strings"
	"testing"
)

func TestChunkTextKeepsShortParagraphsWhole(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph, still short."
	chunks := chunkText(text)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %v, want 2", chunks)
	}
	if chunks[0] != "First paragraph." || chunks[1] != "Second paragraph, still short." {
		t.Errorf("chunks = %#v", chunks)
	}
}

func TestChunkTextSplitsOversizedParagraphBySentence(t *testing.T) {
	sentence := "The fuse for the headlights is number twelve in the driver side panel. "
	// Repeat well past maxChunkChars so the paragraph must be split.
	text := strings.Repeat(sentence, 40)
	chunks := chunkText(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for an oversized paragraph, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > maxChunkChars {
			t.Errorf("chunk exceeds maxChunkChars: %d runes", len([]rune(chunk)))
		}
		if !strings.HasSuffix(strings.TrimSpace(chunk), ".") {
			t.Errorf("chunk does not end on a sentence boundary: %q", chunk)
		}
	}
	// No sentence should have been cut in half.
	rejoined := strings.Join(chunks, " ")
	if strings.Count(rejoined, "fuse for the headlights") != strings.Count(text, "fuse for the headlights") {
		t.Error("sentence count changed across chunking — a sentence was likely cut")
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if chunks := chunkText("   \n\n  "); chunks != nil {
		t.Errorf("chunks = %#v, want nil for blank input", chunks)
	}
}
