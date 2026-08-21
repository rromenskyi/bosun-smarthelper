package documents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/embeddings"
)

// fixedVectorEmbed returns an embeddings.Client whose vector for a given
// Embed call is looked up by the first matching substring key in vectors —
// lets a test control exactly how similar two chunks' embeddings are
// without a real embeddings server.
func fixedVectorEmbed(t *testing.T, vectors map[string][]float64) *embeddings.Client {
	t.Helper()
	embed := embeddings.NewClient(&config.EmbeddingsConfig{BaseURL: "http://embed.test/v1", Model: "embed"})
	embed.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Input string `json:"input"`
		}
		raw, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode embeddings request: %v", err)
		}
		var vector []float64
		for key, v := range vectors {
			if strings.Contains(body.Input, key) {
				vector = v
				break
			}
		}
		if vector == nil {
			t.Fatalf("no fixed vector configured for input %q", body.Input)
		}
		payload, _ := json.Marshal(map[string]any{"data": []map[string]any{{"embedding": vector}}})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(payload))),
		}, nil
	}))
	return embed
}

func TestAttachOrphanedImagesMergesGoodMatch(t *testing.T) {
	embed := fixedVectorEmbed(t, map[string][]float64{
		"wiper motor procedure":  {1, 0},
		"wiper motor diagram":    {0.95, 0.05},
		"unrelated bread recipe": {0, 1},
	})
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), embed)
	ctx := context.Background()

	textSummary, err := store.AddPages(ctx, "Wiper Systems", []PageInput{{Text: "wiper motor procedure"}})
	if err != nil {
		t.Fatalf("add text doc: %v", err)
	}
	if _, err := store.AddPages(ctx, "Wiper Systems (Diagrams)", []PageInput{
		{Text: "wiper motor diagram", ImageURL: "/document-images/wiper.png"},
	}); err != nil {
		t.Fatalf("add image doc: %v", err)
	}
	if _, err := store.AddPages(ctx, "Cookbook", []PageInput{{Text: "unrelated bread recipe"}}); err != nil {
		t.Fatalf("add unrelated doc: %v", err)
	}

	summary, err := store.AttachOrphanedImages(ctx, 0.7)
	if err != nil {
		t.Fatalf("AttachOrphanedImages: %v", err)
	}
	if summary.Attached != 1 || summary.Unmatched != 0 {
		t.Fatalf("summary = %+v, want 1 attached, 0 unmatched", summary)
	}

	docs, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	textDoc := docs.Documents[textSummary.ID]
	if len(textDoc.Chunks) != 1 || textDoc.Chunks[0].ImageURL != "/document-images/wiper.png" {
		t.Errorf("text chunk = %+v, want the image attached to it", textDoc.Chunks)
	}

	// The diagram document's own chunk must be gone — its only content
	// (the image) now lives on the text chunk; leaving it in place would
	// just be the same "junk pile" duplication this mechanism replaces.
	for docID, doc := range docs.Documents {
		if docID == textSummary.ID {
			continue
		}
		for _, chunk := range doc.Chunks {
			if chunk.ImageURL == "/document-images/wiper.png" {
				t.Errorf("orphaned image chunk for wiper.png still present in doc %q after merging", doc.Title)
			}
		}
	}
}

func TestAttachOrphanedImagesLeavesUnmatchedBelowThreshold(t *testing.T) {
	embed := fixedVectorEmbed(t, map[string][]float64{
		"completely unrelated procedure": {1, 0},
		"some diagram caption":           {0, 1},
	})
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), embed)
	ctx := context.Background()

	if _, err := store.AddPages(ctx, "Manual", []PageInput{{Text: "completely unrelated procedure"}}); err != nil {
		t.Fatalf("add text doc: %v", err)
	}
	imageSummary, err := store.AddPages(ctx, "Manual (Diagrams)", []PageInput{
		{Text: "some diagram caption", ImageURL: "/document-images/x.png"},
	})
	if err != nil {
		t.Fatalf("add image doc: %v", err)
	}

	summary, err := store.AttachOrphanedImages(ctx, 0.7)
	if err != nil {
		t.Fatalf("AttachOrphanedImages: %v", err)
	}
	if summary.Attached != 0 || summary.Unmatched != 1 {
		t.Fatalf("summary = %+v, want 0 attached, 1 unmatched", summary)
	}

	docs, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	imageDoc := docs.Documents[imageSummary.ID]
	if len(imageDoc.Chunks) != 1 || imageDoc.Chunks[0].ImageURL != "/document-images/x.png" {
		t.Errorf("unmatched image chunk should be left standalone, got %+v", imageDoc.Chunks)
	}
}

func TestAttachOrphanedImagesDoesNotDoubleAttachToSameTextChunk(t *testing.T) {
	embed := fixedVectorEmbed(t, map[string][]float64{
		"shared procedure text":  {1, 0},
		"first diagram caption":  {0.99, 0.01},
		"second diagram caption": {0.98, 0.02},
	})
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), embed)
	ctx := context.Background()

	textSummary, err := store.AddPages(ctx, "Manual", []PageInput{{Text: "shared procedure text"}})
	if err != nil {
		t.Fatalf("add text doc: %v", err)
	}
	if _, err := store.AddPages(ctx, "Manual (Diagrams)", []PageInput{
		{Text: "first diagram caption", ImageURL: "/document-images/first.png"},
		{Text: "second diagram caption", ImageURL: "/document-images/second.png"},
	}); err != nil {
		t.Fatalf("add image doc: %v", err)
	}

	summary, err := store.AttachOrphanedImages(ctx, 0.7)
	if err != nil {
		t.Fatalf("AttachOrphanedImages: %v", err)
	}
	// Only one image chunk can occupy the single available text chunk;
	// the other must be reported unmatched rather than silently dropped.
	if summary.Attached != 1 || summary.Unmatched != 1 {
		t.Fatalf("summary = %+v, want exactly 1 attached and 1 unmatched", summary)
	}

	docs, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	textDoc := docs.Documents[textSummary.ID]
	if len(textDoc.Chunks) != 1 || textDoc.Chunks[0].ImageURL == "" {
		t.Fatalf("text chunk = %+v, want exactly one image attached", textDoc.Chunks)
	}
}
