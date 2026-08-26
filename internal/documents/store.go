// Package documents stores long reference text (manuals, how-tos) as
// embedded chunks for semantic search, uploaded only through the web UI —
// never as an LLM-callable tool action, to keep the tool contract small for
// weak local models. See docs/memo-search.md.
package documents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/embeddings"
)

// Chunk is one embedded piece of a document. ImageURL is optional — set
// when the source page's real content is a diagram/photo rather than text
// (e.g. a fuse panel chart), so search can still surface it via its title
// text even though there's little to embed.
type Chunk struct {
	Text      string    `json:"text"`
	ImageURL  string    `json:"image_url,omitempty"`
	Embedding []float32 `json:"embedding,omitempty"`
}

// PageInput is one pre-segmented page for AddPages — unlike Add, the
// caller controls chunk boundaries directly (one Chunk per PageInput)
// instead of relying on chunkText's paragraph/sentence splitting.
type PageInput struct {
	Text     string
	ImageURL string
}

// Record is one uploaded document.
type Record struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	CreatedAt string  `json:"created_at"`
	Chunks    []Chunk `json:"chunks"`
	// SourcePath is the internal/filedump tree path this document was
	// ingested from (e.g. "docs/ford/generator-repair"), empty for
	// anything uploaded before that feature existed or added through
	// handleDocumentAddPages' scripted path — search results fall back
	// to just the title in that case, same as always.
	SourcePath string `json:"source_path,omitempty"`
}

// Summary is a Record without chunk text/embeddings, for listing.
type Summary struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	CreatedAt  string `json:"created_at"`
	ChunkCount int    `json:"chunk_count"`
}

// ScoredChunk is one Search result.
type ScoredChunk struct {
	DocumentID    string  `json:"document_id"`
	DocumentTitle string  `json:"document_title"`
	SourcePath    string  `json:"source_path,omitempty"`
	Text          string  `json:"text"`
	ImageURL      string  `json:"image_url,omitempty"`
	Score         float64 `json:"relevance"`
}

type storeFile struct {
	Documents map[string]Record `json:"documents"`
}

// Store persists documents to a local JSON file, atomically, the same
// pattern as internal/tools/memo.go's memo store.
type Store struct {
	path  string
	embed *embeddings.Client
	mu    sync.Mutex
}

// ImagesDir is where diagram/photo files referenced by a Chunk's ImageURL
// live on disk — a sibling directory to the JSON store file. Callers that
// place image files there (see AddPages) are expected to construct
// ImageURL values the web server can actually serve from this directory.
func (s *Store) ImagesDir() string {
	return filepath.Join(filepath.Dir(s.path), "document-images")
}

// NewStore creates a document store. embed may be nil — see
// embeddings.NewClient for what that degrades to.
func NewStore(path string, embed *embeddings.Client) *Store {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".local", "share", "bosun", "documents.json")
		} else {
			path = "documents.json"
		}
	}
	return &Store{path: path, embed: embed}
}

// Add chunks and embeds text, then stores it under title. sourcePath is the
// internal/filedump tree path this text was ingested from, or "" for
// anything not coming through that feature.
func (s *Store) Add(ctx context.Context, title, text, sourcePath string) (Summary, error) {
	pieces := chunkText(text)
	if len(pieces) == 0 {
		return Summary{}, fmt.Errorf("document text is empty")
	}
	pages := make([]PageInput, len(pieces))
	for i, piece := range pieces {
		pages[i] = PageInput{Text: piece}
	}
	return s.AddPages(ctx, title, pages, sourcePath)
}

// AddPages stores one Chunk per PageInput, each embedded independently —
// unlike Add, chunk boundaries are the caller's, not chunkText's. Used for
// ingesting pre-segmented sources (e.g. one manual page per PageInput) that
// may carry an image with little or no text of their own. sourcePath is the
// internal/filedump tree path this document was ingested from, or "" for
// anything not coming through that feature (e.g. handleDocumentAddPages'
// scripted import path).
func (s *Store) AddPages(ctx context.Context, title string, pages []PageInput, sourcePath string) (Summary, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Summary{}, fmt.Errorf("document title is required")
	}
	if len(pages) == 0 {
		return Summary{}, fmt.Errorf("document has no pages")
	}

	id, err := newID()
	if err != nil {
		return Summary{}, fmt.Errorf("generate document id: %w", err)
	}
	record := Record{ID: id, Title: title, CreatedAt: time.Now().Format(time.RFC3339), SourcePath: sourcePath}
	for _, page := range pages {
		chunk := Chunk{Text: page.Text, ImageURL: page.ImageURL}
		if s.embed != nil && strings.TrimSpace(page.Text) != "" {
			if vector, err := s.embed.Embed(ctx, page.Text); err == nil {
				chunk.Embedding = vector
			}
		}
		record.Chunks = append(record.Chunks, chunk)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return Summary{}, err
	}
	data.Documents[id] = record
	if err := s.save(data); err != nil {
		return Summary{}, err
	}
	return summarize(record), nil
}

// List returns all documents, newest first.
func (s *Store) List() ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(data.Documents))
	for _, record := range data.Documents {
		summaries = append(summaries, summarize(record))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].CreatedAt > summaries[j].CreatedAt })
	return summaries, nil
}

// UpdateSourcePath rewrites a document's SourcePath — called by
// internal/filedump when a move/rename carries a RAG-linked file to a new
// tree location, so search results keep pointing at where the file
// actually lives instead of going stale.
func (s *Store) UpdateSourcePath(id, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	record, ok := data.Documents[id]
	if !ok {
		return fmt.Errorf("document %q was not found", id)
	}
	record.SourcePath = path
	data.Documents[id] = record
	return s.save(data)
}

// Delete removes a document by ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := data.Documents[id]; !ok {
		return fmt.Errorf("document %q was not found", id)
	}
	delete(data.Documents, id)
	return s.save(data)
}

// Search ranks chunks by cosine similarity to query, across all documents
// by default or restricted to one when documentID is non-empty — e.g. once
// a caller already knows (from List) which document is relevant, scoping
// to it avoids the rest of a large, unrelated store diluting the results.
// Falls back to a plain substring match — never an error — when embeddings
// are disabled, the embeddings server is unreachable, or no chunk has a
// vector yet.
func (s *Store) Search(ctx context.Context, query string, limit int, documentID string) ([]ScoredChunk, error) {
	if limit <= 0 {
		limit = 5
	}
	s.mu.Lock()
	data, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if documentID != "" {
		if record, ok := data.Documents[documentID]; ok {
			data.Documents = map[string]Record{documentID: record}
		} else {
			data.Documents = nil
		}
	}

	var results []ScoredChunk
	if s.embed != nil {
		if queryVector, err := s.embed.Embed(ctx, query); err == nil {
			for _, record := range data.Documents {
				for _, chunk := range record.Chunks {
					if len(chunk.Embedding) == 0 {
						continue
					}
					score := embeddings.CosineSimilarity(queryVector, chunk.Embedding)
					results = append(results, ScoredChunk{
						DocumentID:    record.ID,
						DocumentTitle: record.Title,
						SourcePath:    record.SourcePath,
						Text:          chunk.Text,
						ImageURL:      chunk.ImageURL,
						Score:         score,
					})
				}
			}
		}
	}
	if results == nil {
		lowerQuery := strings.ToLower(query)
		for _, record := range data.Documents {
			for _, chunk := range record.Chunks {
				if strings.Contains(strings.ToLower(chunk.Text), lowerQuery) {
					results = append(results, ScoredChunk{
						DocumentID:    record.ID,
						DocumentTitle: record.Title,
						SourcePath:    record.SourcePath,
						Text:          chunk.Text,
						ImageURL:      chunk.ImageURL,
						Score:         1,
					})
				}
			}
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func summarize(record Record) Summary {
	return Summary{ID: record.ID, Title: record.Title, CreatedAt: record.CreatedAt, ChunkCount: len(record.Chunks)}
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) load() (storeFile, error) {
	data := storeFile{Documents: make(map[string]Record)}
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("open document store: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return data, fmt.Errorf("decode document store: %w", err)
	}
	if data.Documents == nil {
		data.Documents = make(map[string]Record)
	}
	return data, nil
}

func (s *Store) save(data storeFile) error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create document directory: %w", err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode document store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".documents-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary document store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set document store permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write document store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync document store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close document store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace document store: %w", err)
	}
	return nil
}
