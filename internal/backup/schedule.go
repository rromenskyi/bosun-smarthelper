package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// scheduleStateFile lives in the data directory, next to memos.json etc.
// — deliberately separate from settings.Store: that store's Update does a
// full replace of the whole settings blob on every save (see
// internal/webui/settings.go), so a scheduler-owned bookkeeping field
// living there would get silently reset to zero every time the user saved
// an unrelated setting. This file is written only by RecordRun/read only
// by DueForRun, never touched by the settings HTTP handlers.
const scheduleStateFile = "backup_schedule_state.json"

type scheduleState struct {
	LastRunAt string `json:"last_run_at,omitempty"` // RFC3339
}

// DueForRun reports whether at least intervalHours have passed since the
// last recorded run (or there's no recorded run at all — the automatic
// schedule's very first tick after being enabled should just run).
func DueForRun(dataDir string, intervalHours int, now time.Time) (bool, error) {
	state, err := loadScheduleState(dataDir)
	if err != nil {
		return false, err
	}
	if state.LastRunAt == "" {
		return true, nil
	}
	lastRun, err := time.Parse(time.RFC3339, state.LastRunAt)
	if err != nil {
		return true, nil // an unparseable timestamp shouldn't wedge the schedule forever
	}
	return now.Sub(lastRun) >= time.Duration(intervalHours)*time.Hour, nil
}

// RecordRun stamps now as the last successful automatic run, for the next
// DueForRun check.
func RecordRun(dataDir string, now time.Time) error {
	return atomicWriteJSON(scheduleStatePath(dataDir), scheduleState{LastRunAt: now.UTC().Format(time.RFC3339)})
}

func scheduleStatePath(dataDir string) string {
	return filepath.Join(dataDir, scheduleStateFile)
}

func loadScheduleState(dataDir string) (scheduleState, error) {
	var state scheduleState
	file, err := os.Open(scheduleStatePath(dataDir))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open backup schedule state: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&state); err != nil {
		return state, fmt.Errorf("decode backup schedule state: %w", err)
	}
	return state, nil
}

// atomicWriteJSON writes v to path as indented JSON via a temp file plus
// rename, so a reader (or a crash mid-write) never sees a partially
// written file — the same pattern internal/tools' memo store and
// internal/documents' store already use.
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
