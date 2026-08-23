package sandbox

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestSessionTrackerPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	tracker, err := NewSessionTracker(dir)
	if err != nil {
		t.Fatalf("NewSessionTracker: %v", err)
	}
	if err := tracker.Touch(validSession1); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	reloaded, err := NewSessionTracker(dir)
	if err != nil {
		t.Fatalf("reload NewSessionTracker: %v", err)
	}
	if len(reloaded.Expired(time.Now(), 0)) != 1 {
		t.Errorf("reloaded tracker doesn't see the persisted session")
	}
}

func TestSweepRemovesSessionsIdlePastTTL(t *testing.T) {
	runner := newFakeRunner()
	s := newTestServer(t, runner)
	postRun(t, s, map[string]any{"session_id": validSession1, "code": "pass"})

	// Backdate the touch so it reads as already-expired.
	s.Tracker.mu.Lock()
	s.Tracker.lastUsed[validSession1] = time.Now().Add(-1 * time.Hour)
	s.Tracker.mu.Unlock()

	sweep(context.Background(), s, 5*time.Minute, discardLogger())

	if runner.running["bosun-sandbox-"+validSession1] {
		t.Error("expired session's container was not removed")
	}
	if len(s.Tracker.Expired(time.Now(), 0)) != 0 {
		t.Error("expired session is still tracked after being reaped")
	}
}

func TestSweepKeepsSessionsWithinTTL(t *testing.T) {
	runner := newFakeRunner()
	s := newTestServer(t, runner)
	postRun(t, s, map[string]any{"session_id": validSession1, "code": "pass"})

	sweep(context.Background(), s, 5*time.Minute, discardLogger())

	if !runner.running["bosun-sandbox-"+validSession1] {
		t.Error("fresh session should not have been reaped")
	}
}

func TestSweepRemovesOrphanedScratchDirWithNoTrackedSession(t *testing.T) {
	runner := newFakeRunner()
	s := newTestServer(t, runner)
	orphan := filepath.Join(s.ScratchRoot, validSession2)
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sweep(context.Background(), s, 5*time.Minute, discardLogger())

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphaned scratch dir (no tracked session) should have been removed")
	}
}

func TestReconcileSeedsTrackerFromExistingContainersAndDropsStaleEntries(t *testing.T) {
	runner := newFakeRunner()
	tracker, err := NewSessionTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionTracker: %v", err)
	}
	// A session persisted from a previous run whose container is gone —
	// Reconcile must drop it, not reap it as if it still existed.
	if err := tracker.Touch(validSession2); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	// A container that exists in Docker but wasn't in the persisted state
	// (e.g. state file lost) — Reconcile must pick it up as just-used.
	runner.created["bosun-sandbox-"+validSession1] = containerInfo{Name: "bosun-sandbox-" + validSession1, CreatedAt: time.Now()}

	if err := Reconcile(context.Background(), runner, tracker); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	snapshot := tracker.Snapshot()
	if _, ok := snapshot[validSession1]; !ok {
		t.Error("Reconcile didn't seed the untracked-but-existing container")
	}
	if _, ok := snapshot[validSession2]; ok {
		t.Error("Reconcile didn't drop the stale entry for a container that no longer exists")
	}
}
