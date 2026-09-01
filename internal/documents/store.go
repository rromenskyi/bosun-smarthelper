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
	SourcePath string `json:"source_path,omitempty"`
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
// pattern as internal/tools/memo.go's memo store. cache holds the decoded
// contents in memory once loaded (nil until the first call): every method
// used to decode the whole file from disk on every single call, including
// read-only ones, which cost ~935ms on the live ~50MB store — and a write
// paid that plus a further ~800ms to re-encode it, so a bulk import (one
// Add/AddPages call per file, e.g. examples/import-manual's 1074 diagram
// uploads) did that full round-trip once per file. Keeping the decoded
// map in memory removes the decode cost from every call after the first;
// a write still re-encodes and rewrites the whole file (no incremental
// format), but skips straight to that instead of decoding first.
type Store struct {
	path  string
	embed *embeddings.Client
	mu    sync.Mutex
	cache map[string]Record
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

	if err := s.mutate(func(cache map[string]Record) error {
		cache[id] = record
		return nil
	}); err != nil {
		return Summary{}, err
	}
	return summarize(record), nil
}

// List returns all documents, newest first.
func (s *Store) List() ([]Summary, error) {
	cache, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(cache))
	for _, record := range cache {
		summaries = append(summaries, summarize(record))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].CreatedAt > summaries[j].CreatedAt })
	return summaries, nil
}

// Topics returns the distinct top-level filedump folders at least one
// ingested document lives under (e.g. "hunting-utah" for a document whose
// SourcePath is "hunting-utah/units"), sorted alphabetically. A document
// with no SourcePath (the older scripted AddPages import path predates
// filedump) falls back to its own Title so it isn't silently dropped.
// Grouping by top-level folder, not by individual document title, keeps
// this short even once a folder holds many files — see
// internal/agent.Agent's dynamic topics prompt line, the one consumer.
func (s *Store) Topics() ([]string, error) {
	summaries, err := s.List()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(summaries))
	topics := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		topic := summary.SourcePath
		if idx := strings.Index(topic, "/"); idx >= 0 {
			topic = topic[:idx]
		}
		if topic == "" {
			topic = summary.Title
		}
		if topic == "" || seen[topic] {
			continue
		}
		seen[topic] = true
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	return topics, nil
}

// UpdateSourcePath rewrites a document's SourcePath — called by
// internal/filedump when a move/rename carries a RAG-linked file to a new
// tree location, so search results keep pointing at where the file
// actually lives instead of going stale.
func (s *Store) UpdateSourcePath(id, path string) error {
	return s.mutate(func(cache map[string]Record) error {
		record, ok := cache[id]
		if !ok {
			return fmt.Errorf("document %q was not found", id)
		}
		record.SourcePath = path
		cache[id] = record
		return nil
	})
}

// Delete removes a document by ID.
func (s *Store) Delete(id string) error {
	return s.mutate(func(cache map[string]Record) error {
		if _, ok := cache[id]; !ok {
			return fmt.Errorf("document %q was not found", id)
		}
		delete(cache, id)
		return nil
	})
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
	cache, err := s.snapshot()
	if err != nil {
		return nil, err
	}
	if documentID != "" {
		if record, ok := cache[documentID]; ok {
			cache = map[string]Record{documentID: record}
		} else {
			cache = nil
		}
	}

	var results []ScoredChunk
	if s.embed != nil {
		if queryVector, err := s.embed.Embed(ctx, query); err == nil {
			for _, record := range cache {
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
		for _, record := range cache {
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
	return Summary{ID: record.ID, Title: record.Title, CreatedAt: record.CreatedAt, ChunkCount: len(record.Chunks), SourcePath: record.SourcePath}
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ensureLoadedLocked populates s.cache from disk on the first call and is a
// no-op afterward. Callers must hold s.mu. A decode error leaves s.cache
// nil so the next call retries instead of caching a permanent failure.
func (s *Store) ensureLoadedLocked() error {
	if s.cache != nil {
		return nil
	}
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		s.cache = make(map[string]Record)
		return nil
	}
	if err != nil {
		return fmt.Errorf("open document store: %w", err)
	}
	defer file.Close()
	var data storeFile
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return fmt.Errorf("decode document store: %w", err)
	}
	if data.Documents == nil {
		data.Documents = make(map[string]Record)
	}
	s.cache = data.Documents
	return nil
}

// snapshot returns a shallow copy of the cached document map, loading from
// disk first if needed. A shallow copy is enough for safe concurrent
// reading: a mutation never edits a Record in place, only replaces or
// removes a whole map entry, so a Record value copied out under the lock
// stays valid for a caller working on it after the lock is released — the
// same "grab it under the lock, then work on it unlocked" shape the old
// per-call load() gave every reader for free.
func (s *Store) snapshot() (map[string]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	clone := make(map[string]Record, len(s.cache))
	for id, record := range s.cache {
		clone[id] = record
	}
	return clone, nil
}

// mutate runs fn against the live cache under the lock and persists it
// afterward — but only if fn succeeds, so a "not found" error (Delete,
// UpdateSourcePath) never triggers a pointless rewrite.
func (s *Store) mutate(fn func(map[string]Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}
	if err := fn(s.cache); err != nil {
		return err
	}
	return s.save()
}

// save re-encodes the whole cache and atomically replaces the store file.
// Callers must hold s.mu. Plain Marshal, not MarshalIndent: nothing reads
// this file by eye, and indentation costs real time and disk space once it
// reaches tens of megabytes.
func (s *Store) save() error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create document directory: %w", err)
	}
	payload, err := json.Marshal(storeFile{Documents: s.cache})
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
