package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sessionStateFile lives inside Server.ScratchRoot's sibling state dir —
// same atomicWriteJSON-via-temp-file-plus-rename pattern already used by
// internal/backup/schedule.go and internal/alerts/state.go (duplicated
// here rather than shared across packages, this project's established
// convention for these small state files).
const sessionStateFile = "sandbox_sessions.json"

// sessionTracker records when each session's workspace container was last
// used, so the reaper knows what's actually idle rather than approximating
// it from Docker's own CreatedAt (which would silently extend every
// session's life by up to a full TTL after every sandboxd restart).
type sessionTracker struct {
	mu        sync.Mutex
	lastUsed  map[string]time.Time
	statePath string
}

func NewSessionTracker(stateDir string) (*sessionTracker, error) {
	path := filepath.Join(stateDir, sessionStateFile)
	var lastUsed map[string]time.Time
	if err := loadJSON(path, &lastUsed); err != nil {
		return nil, err
	}
	if lastUsed == nil {
		lastUsed = map[string]time.Time{}
	}
	return &sessionTracker{lastUsed: lastUsed, statePath: path}, nil
}

// Touch records sessionID as used right now, persisting immediately —
// request volume here is low enough (one HTTP call per run_code
// invocation) that this is cheap, and it means a sandboxd crash never
// loses more than the in-flight request's own bookkeeping.
func (t *sessionTracker) Touch(sessionID string) error {
	t.mu.Lock()
	t.lastUsed[sessionID] = time.Now()
	snapshot := t.cloneLocked()
	t.mu.Unlock()
	return atomicWriteJSON(t.statePath, snapshot)
}

// Seed records sessionID as used at a specific time without necessarily
// persisting yet — used during startup reconciliation, where the caller
// persists once after seeding everything it found.
func (t *sessionTracker) Seed(sessionID string, lastUsed time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.lastUsed[sessionID]; !exists {
		t.lastUsed[sessionID] = lastUsed
	}
}

// Forget drops sessionID (the reaper already removed its container and
// scratch dir) and persists the change.
func (t *sessionTracker) Forget(sessionID string) error {
	t.mu.Lock()
	delete(t.lastUsed, sessionID)
	snapshot := t.cloneLocked()
	t.mu.Unlock()
	return atomicWriteJSON(t.statePath, snapshot)
}

// Retain keeps only the given session IDs (startup reconciliation drops
// anything persisted whose container no longer exists), persisting once.
func (t *sessionTracker) Retain(sessionIDs map[string]bool) error {
	t.mu.Lock()
	for id := range t.lastUsed {
		if !sessionIDs[id] {
			delete(t.lastUsed, id)
		}
	}
	snapshot := t.cloneLocked()
	t.mu.Unlock()
	return atomicWriteJSON(t.statePath, snapshot)
}

// Expired returns every session ID idle for at least ttl.
func (t *sessionTracker) Expired(now time.Time, ttl time.Duration) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var expired []string
	for id, last := range t.lastUsed {
		if now.Sub(last) >= ttl {
			expired = append(expired, id)
		}
	}
	return expired
}

// Snapshot returns a copy of the current last-used map, for the orphaned
// scratch-dir sweep to check against.
func (t *sessionTracker) Snapshot() map[string]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cloneLocked()
}

func (t *sessionTracker) cloneLocked() map[string]time.Time {
	clone := make(map[string]time.Time, len(t.lastUsed))
	for id, last := range t.lastUsed {
		clone[id] = last
	}
	return clone
}

// Reconcile seeds the tracker from whatever sandbox containers actually
// exist right now (a sandboxd restart otherwise either immediately
// reaps still-live sessions the persisted state hadn't caught up to, or —
// with the opposite bug — never reaps a session whose persisted entry was
// lost): anything Docker reports that isn't already tracked is treated as
// just-used (conservative — extends its life by one TTL rather than
// risking reaping something mid-use); anything persisted whose container
// no longer exists is dropped.
func Reconcile(ctx context.Context, runner containerRunner, tracker *sessionTracker) error {
	containers, err := runner.ListLabeled(ctx)
	if err != nil {
		return fmt.Errorf("list existing sandbox containers: %w", err)
	}
	seen := make(map[string]bool, len(containers))
	for _, c := range containers {
		sessionID := strings.TrimPrefix(c.Name, "bosun-sandbox-")
		if sessionID == c.Name || !validSessionID(sessionID) {
			continue // not one of ours (shouldn't happen — label-filtered already)
		}
		seen[sessionID] = true
		seededAt := c.CreatedAt
		if seededAt.IsZero() {
			seededAt = time.Now()
		}
		tracker.Seed(sessionID, seededAt)
	}
	return tracker.Retain(seen)
}

// Run ticks every tickInterval, removing any session idle past ttl — the
// same ticker-goroutine shape as cmd/smarthelper/main.go's
// runBackupScheduler/runTagNormalizer.
func Run(ctx context.Context, s *Server, tickInterval, ttl time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep(ctx, s, ttl, logger)
		}
	}
}

func sweep(ctx context.Context, s *Server, ttl time.Duration, logger *slog.Logger) {
	for _, sessionID := range s.Tracker.Expired(time.Now(), ttl) {
		name := "bosun-sandbox-" + sessionID
		if err := s.Runner.Remove(ctx, name); err != nil {
			logger.Warn("remove expired sandbox container", "session", sessionID, "error", err)
			continue // retry next tick rather than forgetting a container removal actually failed on
		}
		if err := os.RemoveAll(filepath.Join(s.ScratchRoot, sessionID)); err != nil {
			logger.Warn("remove expired sandbox workspace", "session", sessionID, "error", err)
		}
		if err := s.Tracker.Forget(sessionID); err != nil {
			logger.Warn("persist sandbox session state after reaping", "session", sessionID, "error", err)
		}
		logger.Info("reaped idle sandbox session", "session", sessionID)
	}
	sweepOrphanedScratchDirs(s, logger)
}

// sweepOrphanedScratchDirs removes workspace directories with no tracked
// session at all — a crash between creating one and ever recording a
// Touch, otherwise a slow one-directional disk leak.
func sweepOrphanedScratchDirs(s *Server, logger *slog.Logger) {
	entries, err := os.ReadDir(s.ScratchRoot)
	if err != nil {
		return // no scratch root yet on a fresh install — nothing to sweep
	}
	tracked := s.Tracker.Snapshot()
	for _, entry := range entries {
		if !entry.IsDir() || !validSessionID(entry.Name()) {
			continue // never touch anything that isn't clearly ours
		}
		if _, ok := tracked[entry.Name()]; ok {
			continue
		}
		path := filepath.Join(s.ScratchRoot, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			logger.Warn("remove orphaned sandbox workspace", "path", path, "error", err)
		} else {
			logger.Info("removed orphaned sandbox workspace", "path", path)
		}
	}
}

func loadJSON(path string, v any) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func atomicWriteJSON(path string, v any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".tmp-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	return nil
}
