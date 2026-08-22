package backup

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDueForRunWithNoPriorRunIsDue(t *testing.T) {
	due, err := DueForRun(t.TempDir(), 24, time.Now())
	if err != nil {
		t.Fatalf("DueForRun: %v", err)
	}
	if !due {
		t.Error("due = false, want true — no prior run recorded at all")
	}
}

func TestDueForRunRespectsInterval(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := RecordRun(dir, now); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	due, err := DueForRun(dir, 24, now.Add(23*time.Hour))
	if err != nil {
		t.Fatalf("DueForRun: %v", err)
	}
	if due {
		t.Error("due = true after only 23h of a 24h interval, want false")
	}

	due, err = DueForRun(dir, 24, now.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("DueForRun: %v", err)
	}
	if !due {
		t.Error("due = false after 25h of a 24h interval, want true")
	}
}

func TestRecordRunPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := RecordRun(dir, now); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	state, err := loadScheduleState(dir)
	if err != nil {
		t.Fatalf("loadScheduleState: %v", err)
	}
	if state.LastRunAt == "" {
		t.Fatal("LastRunAt is empty after RecordRun")
	}
	parsed, err := time.Parse(time.RFC3339, state.LastRunAt)
	if err != nil {
		t.Fatalf("parse LastRunAt: %v", err)
	}
	if parsed.Sub(now).Abs() > time.Second {
		t.Errorf("LastRunAt = %v, want close to %v", parsed, now)
	}
}

func TestScheduleStatePathIsInsideDataDir(t *testing.T) {
	dir := "/some/data/dir"
	want := filepath.Join(dir, "backup_schedule_state.json")
	if got := scheduleStatePath(dir); got != want {
		t.Errorf("scheduleStatePath = %q, want %q", got, want)
	}
}
