package alerts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State files live in the data directory, next to memos.json etc. — kept
// as small, independent JSON files (the same convention
// internal/backup/schedule.go and internal/tools/memo_metric_merge.go
// already use) rather than folded into settings.Store, which does a full
// replace of its whole blob on every save and would silently drop
// scheduler-owned bookkeeping the next time a user saved an unrelated
// setting.
const (
	thresholdStateFile = "alerts_threshold_state.json"
	noaaSeenStateFile  = "alerts_noaa_seen_state.json"
)

// LoadThresholdState returns ThresholdChecker's crossed/not-crossed map as
// of the last SaveThresholdState call, or an empty map if there isn't one
// yet (first run, or a fresh data directory).
func LoadThresholdState(dataDir string) (map[string]bool, error) {
	var state map[string]bool
	if err := loadJSON(filepath.Join(dataDir, thresholdStateFile), &state); err != nil {
		return nil, err
	}
	if state == nil {
		state = map[string]bool{}
	}
	return state, nil
}

// SaveThresholdState persists state for the next LoadThresholdState.
func SaveThresholdState(dataDir string, state map[string]bool) error {
	return atomicWriteJSON(filepath.Join(dataDir, thresholdStateFile), state)
}

// LoadNOAASeenIDs returns the NOAA alert IDs already notified about as of
// the last SaveNOAASeenIDs call.
func LoadNOAASeenIDs(dataDir string) (map[string]bool, error) {
	var ids map[string]bool
	if err := loadJSON(filepath.Join(dataDir, noaaSeenStateFile), &ids); err != nil {
		return nil, err
	}
	if ids == nil {
		ids = map[string]bool{}
	}
	return ids, nil
}

// SaveNOAASeenIDs persists the full set of currently-active alert IDs —
// a full replace, not a merge, so an alert NOAA has stopped reporting as
// active naturally drops out and would be treated as new again if its ID
// were ever reused (NOAA's IDs are unique per issuance, so this is only a
// theoretical concern, not an observed one).
func SaveNOAASeenIDs(dataDir string, ids map[string]bool) error {
	return atomicWriteJSON(filepath.Join(dataDir, noaaSeenStateFile), ids)
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

// atomicWriteJSON writes v to path as indented JSON via a temp file plus
// rename, so a reader (or a crash mid-write) never sees a partially
// written file — the same pattern used throughout this project's other
// small JSON stores (internal/backup/schedule.go, internal/tools/memo.go).
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
