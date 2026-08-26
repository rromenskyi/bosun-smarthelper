package documents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/embeddings"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStoreAddListDelete(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	summary, err := store.Add(ctx, "Car manual", "Fuse 12 controls the headlights.\n\nFuse 7 controls the radio.", "")
	if err != nil {
		t.Fatalf("add document: %v", err)
	}
	if summary.ChunkCount != 2 {
		t.Errorf("chunk count = %d, want 2", summary.ChunkCount)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(list) != 1 || list[0].ID != summary.ID {
		t.Fatalf("list = %#v", list)
	}

	if err := store.Delete(summary.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	list, err = store.List()
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list after delete = %#v, want empty", list)
	}
	if err := store.Delete(summary.ID); err == nil {
		t.Error("expected an error deleting an already-deleted document")
	}
}

func TestStoreAddRejectsBlankTitleOrText(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()
	if _, err := store.Add(ctx, "", "some text", ""); err == nil {
		t.Error("expected an error for a blank title")
	}
	if _, err := store.Add(ctx, "title", "   ", ""); err == nil {
		t.Error("expected an error for blank text")
	}
}

func TestStoreSearchFallsBackToSubstringWithoutEmbeddings(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()
	if _, err := store.Add(ctx, "Car manual", "Fuse 12 controls the headlights.", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}
	if _, err := store.Add(ctx, "Recipe", "Add two cups of flour.", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}

	results, err := store.Search(ctx, "headlights", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].DocumentTitle != "Car manual" {
		t.Fatalf("results = %#v", results)
	}
}

func TestStoreSearchRanksBySemanticSimilarity(t *testing.T) {
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
		switch {
		case strings.Contains(body.Input, "headlight fuse text"):
			vector = []float64{1, 0}
		case strings.Contains(body.Input, "flour recipe text"):
			vector = []float64{0, 1}
		default: // the search query itself
			vector = []float64{0.9, 0.1}
		}
		payload, _ := json.Marshal(map[string]any{"data": []map[string]any{{"embedding": vector}}})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(payload))),
		}, nil
	}))

	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), embed)
	ctx := context.Background()
	if _, err := store.Add(ctx, "Car manual", "headlight fuse text", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}
	if _, err := store.Add(ctx, "Recipe", "flour recipe text", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}

	results, err := store.Search(ctx, "which fuse is broken", 1, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].DocumentTitle != "Car manual" {
		t.Fatalf("top result = %#v, want Car manual", results)
	}
}

func TestStoreAddPagesWithImage(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	summary, err := store.AddPages(ctx, "Fuse diagrams", []PageInput{
		{Text: "Fuse panel: Locations", ImageURL: "/document-images/fuse-panel.png"},
		{Text: "Fuse panel: Application and ID"},
	}, "")
	if err != nil {
		t.Fatalf("add pages: %v", err)
	}
	if summary.ChunkCount != 2 {
		t.Fatalf("chunk count = %d, want 2", summary.ChunkCount)
	}

	results, err := store.Search(ctx, "Locations", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var found bool
	for _, r := range results {
		if r.ImageURL == "/document-images/fuse-panel.png" {
			found = true
		}
	}
	if !found {
		t.Errorf("results = %#v, want one with the fuse panel image_url", results)
	}
}

func TestStoreSearchScopesToDocumentID(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	carSummary, err := store.Add(ctx, "Car manual", "the headlight fuse is number 12", "")
	if err != nil {
		t.Fatalf("add car manual: %v", err)
	}
	if _, err := store.Add(ctx, "Boat manual", "the headlight fuse is number 3", ""); err != nil {
		t.Fatalf("add boat manual: %v", err)
	}

	results, err := store.Search(ctx, "headlight fuse", 5, carSummary.ID)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].DocumentID != carSummary.ID {
		t.Fatalf("results = %#v, want only the Car manual chunk", results)
	}
}

func TestStoreSearchUnknownDocumentIDReturnsNoResults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()
	if _, err := store.Add(ctx, "Car manual", "the headlight fuse is number 12", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}

	results, err := store.Search(ctx, "headlight fuse", 5, "does-not-exist")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %#v, want none for an unknown document_id", results)
	}
}

func TestStoreAddSourcePathSurfacesInSearch(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	if _, err := store.Add(ctx, "Ford generator manual", "the fuel filter is under the seat", "docs/ford/generator-repair"); err != nil {
		t.Fatalf("add document: %v", err)
	}
	if _, err := store.Add(ctx, "Generic manual", "the fuel filter is under the seat", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}

	results, err := store.Search(ctx, "fuel filter", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var sawFord, sawGeneric bool
	for _, r := range results {
		switch r.DocumentTitle {
		case "Ford generator manual":
			if r.SourcePath != "docs/ford/generator-repair" {
				t.Errorf("Ford result SourcePath = %q, want docs/ford/generator-repair", r.SourcePath)
			}
			sawFord = true
		case "Generic manual":
			if r.SourcePath != "" {
				t.Errorf("Generic result SourcePath = %q, want empty", r.SourcePath)
			}
			sawGeneric = true
		}
	}
	if !sawFord || !sawGeneric {
		t.Fatalf("results = %#v, want both documents", results)
	}
}

func TestStoreUpdateSourcePath(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	summary, err := store.Add(ctx, "Ford generator manual", "the fuel filter is under the seat", "docs/ford/generator-repair")
	if err != nil {
		t.Fatalf("add document: %v", err)
	}

	if err := store.UpdateSourcePath(summary.ID, "archive/ford/generator-repair"); err != nil {
		t.Fatalf("update source path: %v", err)
	}

	results, err := store.Search(ctx, "fuel filter", 5, summary.ID)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].SourcePath != "archive/ford/generator-repair" {
		t.Fatalf("results = %#v, want SourcePath archive/ford/generator-repair", results)
	}

	if err := store.UpdateSourcePath("does-not-exist", "x"); err == nil {
		t.Error("expected an error updating an unknown document")
	}
}

func TestStoreImagesDirIsSiblingOfStoreFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "documents.json"), nil)
	want := filepath.Join(dir, "document-images")
	if got := store.ImagesDir(); got != want {
		t.Errorf("ImagesDir() = %q, want %q", got, want)
	}
}
