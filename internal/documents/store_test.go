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

func TestStoreTopicsGroupsByTopLevelFolderAndFallsBackToTitle(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	if _, err := store.Add(ctx, "Ford generator manual", "text", "docs/ford/generator-repair"); err != nil {
		t.Fatalf("add document: %v", err)
	}
	if _, err := store.Add(ctx, "Ford wiring diagram", "text", "docs/ford/wiring"); err != nil {
		t.Fatalf("add document: %v", err)
	}
	if _, err := store.Add(ctx, "Utah deer regulations", "text", "hunting-utah"); err != nil {
		t.Fatalf("add document: %v", err)
	}
	if _, err := store.Add(ctx, "No source path manual", "text", ""); err != nil {
		t.Fatalf("add document: %v", err)
	}

	topics, err := store.Topics()
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	want := []string{"No source path manual", "docs", "hunting-utah"}
	if len(topics) != len(want) {
		t.Fatalf("topics = %#v, want %#v", topics, want)
	}
	for i, w := range want {
		if topics[i] != w {
			t.Errorf("topics[%d] = %q, want %q (full: %#v)", i, topics[i], w, topics)
		}
	}
}

func TestStoreTopicsEmptyStoreReturnsEmpty(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	topics, err := store.Topics()
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	if len(topics) != 0 {
		t.Errorf("topics = %#v, want empty", topics)
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

// TestStoreWritesSurviveFreshInstance guards the in-memory cache introduced
// to avoid a full disk decode on every call: a second Store pointed at the
// same file must see exactly what the first one wrote, proving mutate()'s
// persisted JSON — not just the first Store's own cache — is the real
// source of truth.
func TestStoreWritesSurviveFreshInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	ctx := context.Background()

	first := NewStore(path, nil)
	kept, err := first.Add(ctx, "Kept", "Some real content here.", "manuals/generator")
	if err != nil {
		t.Fatalf("add kept doc: %v", err)
	}
	removed, err := first.Add(ctx, "Removed", "Content that will be deleted.", "")
	if err != nil {
		t.Fatalf("add removed doc: %v", err)
	}
	if err := first.Delete(removed.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := first.UpdateSourcePath(kept.ID, "manuals/generator-renamed"); err != nil {
		t.Fatalf("update source path: %v", err)
	}

	second := NewStore(path, nil)
	list, err := second.List()
	if err != nil {
		t.Fatalf("list from fresh instance: %v", err)
	}
	if len(list) != 1 || list[0].ID != kept.ID {
		t.Fatalf("list from fresh instance = %#v, want only %q", list, kept.ID)
	}
	if list[0].SourcePath != "manuals/generator-renamed" {
		t.Errorf("source path = %q, want the renamed path to have persisted", list[0].SourcePath)
	}
}

// TestStoreConcurrentAccessDoesNotRace exercises reads and writes from many
// goroutines at once — the in-memory cache this rewrite introduced is
// shared mutable state, unlike the old per-call disk decode where every
// caller worked on its own freshly-parsed copy. Run with -race in CI-style
// verification; without -race this just checks nothing panics or errors.
func TestStoreConcurrentAccessDoesNotRace(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	seed, err := store.Add(ctx, "Seed", "Fuse 12 controls the headlights.", "")
	if err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	const goroutines = 8
	done := make(chan error, goroutines*3)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			_, err := store.Add(ctx, "Concurrent", "Some searchable content.", "")
			done <- err
		}(i)
		go func() {
			_, err := store.List()
			done <- err
		}()
		go func() {
			_, err := store.Search(ctx, "fuse", 5, "")
			done <- err
		}()
	}
	for i := 0; i < goroutines*3; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if len(list) != goroutines+1 {
		t.Errorf("final document count = %d, want %d", len(list), goroutines+1)
	}
	found := false
	for _, s := range list {
		if s.ID == seed.ID {
			found = true
		}
	}
	if !found {
		t.Error("seed document missing after concurrent access")
	}
}

func TestAddManyPagesAddsEveryDocumentInOneCall(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	summaries, err := store.AddManyPages(ctx, []DocumentSpec{
		{Title: "First", Pages: []PageInput{{Text: "fuse panel diagram"}}, SourcePath: "manuals/a"},
		{Title: "Second", Pages: []PageInput{{Text: "wiring page one"}, {Text: "wiring page two"}}, SourcePath: "manuals/b"},
	})
	if err != nil {
		t.Fatalf("AddManyPages: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2", len(summaries))
	}
	if summaries[0].Title != "First" || summaries[0].ChunkCount != 1 || summaries[0].SourcePath != "manuals/a" {
		t.Errorf("summaries[0] = %#v", summaries[0])
	}
	if summaries[1].Title != "Second" || summaries[1].ChunkCount != 2 || summaries[1].SourcePath != "manuals/b" {
		t.Errorf("summaries[1] = %#v", summaries[1])
	}
	if summaries[0].ID == summaries[1].ID {
		t.Error("both documents got the same ID")
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() = %d documents, want 2", len(list))
	}
}

func TestAddManyPagesRejectsEmptyBatch(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	if _, err := store.AddManyPages(context.Background(), nil); err == nil {
		t.Error("expected an error for an empty batch")
	}
}

// TestAddManyPagesValidatesEveryDocumentBeforeAddingAny locks in the
// all-or-nothing contract: a bad entry anywhere in the batch must leave
// the store completely untouched, not partially populated by whatever
// came before it.
func TestAddManyPagesValidatesEveryDocumentBeforeAddingAny(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	ctx := context.Background()

	_, err := store.AddManyPages(ctx, []DocumentSpec{
		{Title: "Valid", Pages: []PageInput{{Text: "real content"}}},
		{Title: "", Pages: []PageInput{{Text: "irrelevant"}}},
	})
	if err == nil {
		t.Fatal("expected an error for a spec with a blank title")
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() = %#v, want empty — a failed batch must add nothing", list)
	}
}

func TestAddManyPagesRejectsDocumentWithNoPages(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "documents.json"), nil)
	_, err := store.AddManyPages(context.Background(), []DocumentSpec{
		{Title: "Empty", Pages: nil},
	})
	if err == nil {
		t.Error("expected an error for a document with no pages")
	}
}

// TestAddManyPagesWritesSurviveFreshInstance guards that a batched write
// is persisted just as durably as AddPages's per-document write — the
// whole point of batching is skipping *redundant* flushes, not skipping
// the flush itself.
func TestAddManyPagesWritesSurviveFreshInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	first := NewStore(path, nil)
	if _, err := first.AddManyPages(context.Background(), []DocumentSpec{
		{Title: "A", Pages: []PageInput{{Text: "content a"}}},
		{Title: "B", Pages: []PageInput{{Text: "content b"}}},
	}); err != nil {
		t.Fatalf("AddManyPages: %v", err)
	}

	second := NewStore(path, nil)
	list, err := second.List()
	if err != nil {
		t.Fatalf("List from fresh instance: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list from fresh instance = %d documents, want 2", len(list))
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
