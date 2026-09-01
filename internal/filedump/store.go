// Package filedump stores arbitrary files as-is in a real folder tree on
// disk — unlike internal/documents (which only ever holds embedded text
// chunks, never the original file), this is a general-purpose file store
// browsable/organizable through the web UI. A file can optionally also be
// fed into internal/documents for semantic search; this package tracks
// only *whether* that happened and which documents.Record it produced
// (a small sidecar index — see Store.sidecarPath), never document content
// itself. See docs/filedump.md.
package filedump

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FolderInfo is one subfolder in a Listing.
type FolderInfo struct {
	Name string `json:"name"`
}

// FileInfo is one file in a Listing.
type FileInfo struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModTime    string `json:"mtime"`
	InRAG      bool   `json:"in_rag"`
	DocumentID string `json:"document_id,omitempty"`
	// RAGPending/RAGError reflect asynchronous ingestion (see
	// docs/filedump.md) — set by SetPending/SetIngestError, not by List
	// itself. At most one of RAGPending/RAGError/InRAG is meaningfully
	// true at a time for a given file: pending while ingestion runs in
	// the background, then either InRAG (success) or RAGError (failure)
	// once it finishes.
	RAGPending bool   `json:"rag_pending,omitempty"`
	RAGError   string `json:"rag_error,omitempty"`
}

// Listing is the contents of one directory.
type Listing struct {
	Folders []FolderInfo `json:"folders"`
	Files   []FileInfo   `json:"files"`
}

// LinkUpdate describes one sidecar entry whose path changed as a side
// effect of Move — the caller (internal/webui/filedump.go) uses this to
// keep documents.Record.SourcePath in sync via
// documents.Store.UpdateSourcePath, since this package deliberately
// doesn't import internal/documents itself.
type LinkUpdate struct {
	DocumentID string
	NewPath    string
}

// DeleteResult reports what a Delete actually removed, so the caller can
// cascade-delete the right documents.Record IDs and so the web UI's
// confirm() dialog can name an accurate count before the user commits.
type DeleteResult struct {
	Paths       []string // every raw-file path removed (relative, forward-slash)
	DocumentIDs []string // documents.Record IDs to also delete
}

type sidecarFile struct {
	// Links maps a tree-relative path (forward-slash, no leading slash)
	// to the documents.Record ID it was ingested into.
	Links map[string]string `json:"links"`
	// Pending marks a path whose RAG ingestion is running in a background
	// goroutine (see internal/webui/filedump.go's
	// ingestFileDumpUploadAsync) — set right after the raw upload
	// completes and the HTTP response has already gone back to the
	// client, cleared once ingestion finishes either way. Ingestion
	// used to run inline before the response was sent, which meant a
	// slow OCR-heavy PDF held the client's connection open long enough
	// to hit an intermediate proxy's own timeout (confirmed live:
	// Cloudflare Tunnel's ~100s edge timeout) well before
	// web.request_timeout (600s) ever would.
	Pending map[string]bool `json:"pending,omitempty"`
	// Errors maps a path to its last ingestion failure's message —
	// surfaced in the file list (FileInfo.RAGError) since a failure can
	// no longer be reported inline in the upload response the way it was
	// when ingestion ran synchronously. Cleared by a subsequent
	// successful ingestion (LinkDocument) or a fresh SetIngestError("").
	Errors map[string]string `json:"errors,omitempty"`
}

// Store manages one file-dump root directory plus its sidecar RAG-link
// index. All methods are safe for concurrent use.
type Store struct {
	root        string
	sidecarPath string
	mu          sync.Mutex
}

// NewStore creates (if needed) and opens a file store rooted at root.
// Empty root resolves to ~/.local/share/bosun/filedump. The sidecar
// index lives as a sibling *file* next to the root *directory* (same
// relationship as documents.Store.ImagesDir, just inverted — here the
// tree is the primary data and the JSON is the sidecar), so it's never
// itself a browsable entry inside the tree.
func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".local", "share", "bosun", "filedump")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve file store root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create file store root: %w", err)
	}
	sidecarPath := filepath.Join(filepath.Dir(absRoot), "filedump-index.json")
	return &Store{root: absRoot, sidecarPath: sidecarPath}, nil
}

// Root is the absolute directory http.FileServer should serve downloads
// from (internal/webui/server.go's GET /files/ route).
func (s *Store) Root() string {
	return s.root
}

// List returns the immediate contents of relDir (empty for the root).
func (s *Store) List(relDir string) (Listing, error) {
	dir, err := safeJoin(s.root, relDir)
	if err != nil {
		return Listing{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Listing{}, nil
		}
		return Listing{}, fmt.Errorf("list directory: %w", err)
	}
	sidecar, err := s.loadSidecar()
	if err != nil {
		return Listing{}, err
	}

	listing := Listing{Folders: []FolderInfo{}, Files: []FileInfo{}}
	base := cleanRelPath(relDir)
	for _, entry := range entries {
		if entry.IsDir() {
			listing.Folders = append(listing.Folders, FolderInfo{Name: entry.Name()})
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		entryRel := entry.Name()
		if base != "" {
			entryRel = base + "/" + entry.Name()
		}
		fileInfo := FileInfo{
			Name:    entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		}
		if docID, ok := sidecar.Links[entryRel]; ok {
			fileInfo.InRAG = true
			fileInfo.DocumentID = docID
		}
		if sidecar.Pending[entryRel] {
			fileInfo.RAGPending = true
		}
		if message, ok := sidecar.Errors[entryRel]; ok {
			fileInfo.RAGError = message
		}
		listing.Files = append(listing.Files, fileInfo)
	}
	sort.Slice(listing.Folders, func(i, j int) bool { return listing.Folders[i].Name < listing.Folders[j].Name })
	sort.Slice(listing.Files, func(i, j int) bool { return listing.Files[i].Name < listing.Files[j].Name })
	return listing, nil
}

// CreateFolder makes a new subfolder of relDir named name.
func (s *Store) CreateFolder(relDir, name string) error {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("folder name must not be empty or contain a path separator")
	}
	dir, err := safeJoin(s.root, relDir)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	if err := safeWithin(s.root, target); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	return nil
}

// OpenForWrite opens (creating or truncating) relDir/filename for a
// caller to stream an upload's bytes into, returning the file handle and
// the resulting tree-relative path. filename must be a bare name, not a
// path — callers upload into a specific target folder via relDir, they
// don't get to redirect elsewhere via the filename field.
func (s *Store) OpenForWrite(relDir, filename string) (*os.File, string, error) {
	if filename == "" || strings.ContainsAny(filename, "/\\") {
		return nil, "", fmt.Errorf("file name must not be empty or contain a path separator")
	}
	dir, err := safeJoin(s.root, relDir)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create target folder: %w", err)
	}
	target := filepath.Join(dir, filename)
	if err := safeWithin(s.root, target); err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create file: %w", err)
	}
	rel, err := relPath(s.root, target)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, rel, nil
}

// LinkDocument records that relPath was ingested into documents.Record
// documentID — called after a successful RAG ingestion, never before,
// since a failed ingestion must leave the raw file's upload looking
// exactly like any other non-RAG upload.
func (s *Store) LinkDocument(relFilePath, documentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sidecar, err := s.loadSidecar()
	if err != nil {
		return err
	}
	key := cleanRelPath(relFilePath)
	sidecar.Links[key] = documentID
	delete(sidecar.Pending, key)
	delete(sidecar.Errors, key)
	return s.saveSidecar(sidecar)
}

// SetPending marks (or clears) relFilePath as having a RAG ingestion
// running in the background — see sidecarFile.Pending.
func (s *Store) SetPending(relFilePath string, pending bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sidecar, err := s.loadSidecar()
	if err != nil {
		return err
	}
	key := cleanRelPath(relFilePath)
	if pending {
		sidecar.Pending[key] = true
	} else {
		delete(sidecar.Pending, key)
	}
	return s.saveSidecar(sidecar)
}

// SetIngestError records a background ingestion's failure message for
// relFilePath (clearing Pending either way), or clears a previously
// recorded one when message is "". See sidecarFile.Errors.
func (s *Store) SetIngestError(relFilePath, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sidecar, err := s.loadSidecar()
	if err != nil {
		return err
	}
	key := cleanRelPath(relFilePath)
	delete(sidecar.Pending, key)
	if message == "" {
		delete(sidecar.Errors, key)
	} else {
		sidecar.Errors[key] = message
	}
	return s.saveSidecar(sidecar)
}

// DocumentID returns the documents.Record ID relFilePath was ingested
// into, if any.
func (s *Store) DocumentID(relFilePath string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sidecar, err := s.loadSidecar()
	if err != nil {
		return "", false, err
	}
	id, ok := sidecar.Links[cleanRelPath(relFilePath)]
	return id, ok, nil
}

// Move renames/moves fromRel to toRel (a file or a folder — a folder
// move can carry many sidecar-linked files with it). Returns the set of
// sidecar links that moved, so the caller can update the corresponding
// documents.Record.SourcePath values; this package doesn't import
// internal/documents itself.
func (s *Store) Move(fromRel, toRel string) ([]LinkUpdate, error) {
	fromAbs, err := safeJoin(s.root, fromRel)
	if err != nil {
		return nil, err
	}
	toAbs, err := safeJoin(s.root, toRel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o700); err != nil {
		return nil, fmt.Errorf("create destination folder: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(fromAbs); err != nil {
		return nil, fmt.Errorf("source does not exist: %w", err)
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		return nil, fmt.Errorf("move: %w", err)
	}

	fromKey := cleanRelPath(fromRel)
	toKey := cleanRelPath(toRel)

	sidecar, err := s.loadSidecar()
	if err != nil {
		return nil, err
	}
	var updates []LinkUpdate
	for path, docID := range sidecar.Links {
		var newPath string
		switch {
		case path == fromKey:
			newPath = toKey
		case strings.HasPrefix(path, fromKey+"/"):
			newPath = toKey + strings.TrimPrefix(path, fromKey)
		default:
			continue
		}
		delete(sidecar.Links, path)
		sidecar.Links[newPath] = docID
		updates = append(updates, LinkUpdate{DocumentID: docID, NewPath: newPath})
	}
	if len(updates) > 0 {
		if err := s.saveSidecar(sidecar); err != nil {
			return nil, err
		}
	}
	return updates, nil
}

// Delete removes relFilePath. recursive must be true to remove a
// non-empty folder — a plain file or an already-empty folder ignores it.
// Returns every raw path removed and every documents.Record ID that was
// linked to something under it, for the caller to cascade-delete.
func (s *Store) Delete(rel string, recursive bool) (DeleteResult, error) {
	target, err := safeJoin(s.root, rel)
	if err != nil {
		return DeleteResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("stat target: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sidecar, err := s.loadSidecar()
	if err != nil {
		return DeleteResult{}, err
	}

	key := cleanRelPath(rel)
	result := DeleteResult{}

	if !info.IsDir() {
		if err := os.Remove(target); err != nil {
			return DeleteResult{}, fmt.Errorf("remove file: %w", err)
		}
		result.Paths = append(result.Paths, key)
		if docID, ok := sidecar.Links[key]; ok {
			result.DocumentIDs = append(result.DocumentIDs, docID)
			delete(sidecar.Links, key)
		}
		return result, s.saveSidecar(sidecar)
	}

	if !recursive {
		if err := os.Remove(target); err != nil { // fails if non-empty, which is the point
			return DeleteResult{}, fmt.Errorf("folder is not empty (recursive delete not requested): %w", err)
		}
		return result, nil
	}

	err = filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := relPath(s.root, path)
		if err != nil {
			return err
		}
		result.Paths = append(result.Paths, rel)
		if docID, ok := sidecar.Links[rel]; ok {
			result.DocumentIDs = append(result.DocumentIDs, docID)
			delete(sidecar.Links, rel)
		}
		return nil
	})
	if err != nil {
		return DeleteResult{}, fmt.Errorf("walk folder before delete: %w", err)
	}
	if err := os.RemoveAll(target); err != nil {
		return DeleteResult{}, fmt.Errorf("remove folder: %w", err)
	}
	return result, s.saveSidecar(sidecar)
}

func (s *Store) loadSidecar() (sidecarFile, error) {
	data := sidecarFile{Links: make(map[string]string), Pending: make(map[string]bool), Errors: make(map[string]string)}
	file, err := os.Open(s.sidecarPath)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("open file-dump index: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return data, fmt.Errorf("decode file-dump index: %w", err)
	}
	if data.Links == nil {
		data.Links = make(map[string]string)
	}
	if data.Pending == nil {
		data.Pending = make(map[string]bool)
	}
	if data.Errors == nil {
		data.Errors = make(map[string]string)
	}
	return data, nil
}

// saveSidecar writes the index atomically (temp file + rename), the same
// pattern as internal/documents/store.go's save — safe against a crash
// mid-write leaving a corrupt index behind.
func (s *Store) saveSidecar(data sidecarFile) error {
	directory := filepath.Dir(s.sidecarPath)
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode file-dump index: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".filedump-index-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file-dump index: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set file-dump index permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write file-dump index: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync file-dump index: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close file-dump index: %w", err)
	}
	if err := os.Rename(temporaryPath, s.sidecarPath); err != nil {
		return fmt.Errorf("replace file-dump index: %w", err)
	}
	return nil
}

// safeWithin re-checks an already-joined absolute path against root —
// used after appending a caller-controlled leaf name (folder/file name)
// to a safeJoin'd directory, since that leaf itself could still contain
// something like "..\\" on a filesystem that treats backslash specially,
// or simply be "." — cheap insurance on top of the empty/separator
// checks callers already do on the raw name.
func safeWithin(root, absPath string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	resolved, err := filepath.Abs(absPath)
	if err != nil {
		return err
	}
	if resolved != absRoot && !strings.HasPrefix(resolved, absRoot+string(filepath.Separator)) {
		return fmt.Errorf("resulting path escapes the file store root")
	}
	return nil
}
