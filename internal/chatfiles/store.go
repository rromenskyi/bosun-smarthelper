// Package chatfiles stores files a user attaches directly to a chat
// message — temporarily, until the chat_file tool (internal/tools) does
// something with one (add_to_rag, add_to_memo) or a TTL reaper cleans it
// up unclaimed. Deliberately separate from internal/filedump: a filedump
// upload is a deliberate, permanent addition to a browsable tree; a chat
// attachment is disposable scratch input, gone within an hour whether or
// not anything happened to it.
package chatfiles

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/errlog"
)

// maxFilesPerSession bounds how many files one chat session can have
// attached at once — a personal appliance's chat compose bar, not a bulk
// upload surface; internal/filedump already exists for that.
const maxFilesPerSession = 20

// FileInfo is one attached file's identity, for listing.
type FileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Store manages one root directory of per-session attachment
// subdirectories. All methods are safe for concurrent use.
type Store struct {
	root string
	mu   sync.Mutex
}

// NewStore creates (if needed) a store rooted at root. Empty root
// resolves to a subdirectory of os.TempDir() — deliberately not under the
// persistent data directory (internal/filedump, internal/documents):
// nothing here is meant to survive a restart, so it has no reason to be
// backed up or bind-mounted either.
func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(os.TempDir(), "bosun-chat-files")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve chat files root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create chat files root: %w", err)
	}
	return &Store{root: absRoot}, nil
}

// validSessionID matches webui's own session ID format (hex/alnum plus
// dash/underscore, no path separators) — this package's own defense
// against path traversal via a malformed session ID, not something it
// trusts a caller to have already checked, the same reasoning as
// internal/filedump/path.go's safeJoin.
func validSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			continue
		default:
			return false
		}
	}
	return true
}

// resolve joins sessionID/filename under root and verifies the result
// didn't escape root.
func (s *Store) resolve(sessionID, filename string) (string, error) {
	if !validSessionID(sessionID) {
		return "", fmt.Errorf("invalid session id")
	}
	name := filepath.Base(filename)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("file name is required")
	}
	dir := filepath.Join(s.root, sessionID)
	path := filepath.Join(dir, name)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve session directory: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	if absPath != filepath.Join(absDir, name) {
		return "", fmt.Errorf("invalid file name")
	}
	return absPath, nil
}

// Save writes content under sessionID, named filename (basename only —
// any directory component is stripped). Returns the sanitized name
// actually used. Fails if the session already has maxFilesPerSession
// files attached; overwrites a same-named file already attached (a
// re-send is treated as "replace what I just sent", not an error).
func (s *Store) Save(sessionID, filename string, r io.Reader) (string, error) {
	path, err := s.resolve(sessionID, filename)
	if err != nil {
		return "", err
	}
	name := filepath.Base(path)

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", fmt.Errorf("list session directory: %w", err)
		}
		if len(entries) >= maxFilesPerSession {
			return "", fmt.Errorf("this chat already has %d files attached, the most allowed at once", maxFilesPerSession)
		}
	}

	dest, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create attached file: %w", err)
	}
	defer dest.Close()
	if _, err := io.Copy(dest, r); err != nil {
		return "", fmt.Errorf("write attached file: %w", err)
	}
	return name, nil
}

// List returns every file currently attached to sessionID, alphabetical.
// A session with nothing attached (including one that never existed)
// returns an empty slice, not an error.
func (s *Store) List(sessionID string) ([]FileInfo, error) {
	if !validSessionID(sessionID) {
		return nil, fmt.Errorf("invalid session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, sessionID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list attached files: %w", err)
	}
	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{Name: entry.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// Read returns the full content of sessionID's file named filename.
func (s *Store) Read(sessionID, filename string) ([]byte, error) {
	path, err := s.resolve(sessionID, filename)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no file named %q is attached to this chat", filename)
	}
	if err != nil {
		return nil, fmt.Errorf("read attached file: %w", err)
	}
	return content, nil
}

// Forget removes one attached file — called once its content has been
// consumed (added to RAG or a memo), so the same file isn't offered
// twice and doesn't linger until the TTL reaper gets to it. Removing an
// already-gone file is not an error.
func (s *Store) Forget(sessionID, filename string) error {
	path, err := s.resolve(sessionID, filename)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove attached file: %w", err)
	}
	return nil
}

// Reap removes every session subdirectory whose most recent modification
// is older than ttl. A directory's own mtime already reflects when a
// file was last attached to it (the only kind of activity this package
// has), so unlike internal/sandbox's reaper this needs no separate
// tracked-vs-actual reconciliation.
func (s *Store) Reap(ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("list chat files root: %w", err)
	}
	cutoff := time.Now().Add(-ttl)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.root, entry.Name())); err != nil {
			return fmt.Errorf("remove expired session %q: %w", entry.Name(), err)
		}
	}
	return nil
}

// Run ticks Reap every interval until ctx is cancelled — same
// ticker-goroutine shape as internal/sandbox.Run and
// cmd/smarthelper/background.go's schedulers.
func Run(ctx context.Context, store *Store, interval, ttl time.Duration, logger *slog.Logger, errLog *errlog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.Reap(ttl); err != nil {
				logger.Warn("reap expired chat file attachments", "error", err)
				errLog.Record("chat_files_reaper", "reap", err)
			}
		}
	}
}
