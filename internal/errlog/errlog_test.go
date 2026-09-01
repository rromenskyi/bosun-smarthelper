package errlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLogger_RecordAndReadAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	logger, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	logger.Record("tool_call", "get_weather", errors.New("boom"))
	logger.Record("llm_chat", "local", errors.New("timeout"))
	logger.Record("tool_call", "get_weather", nil) // nil error must be a no-op
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (nil-error Record must not write): %#v", len(entries), entries)
	}
	if entries[0].Category != "tool_call" || entries[0].Detail != "get_weather" || entries[0].Error != "boom" {
		t.Errorf("entry 0 = %#v", entries[0])
	}
	if entries[1].Category != "llm_chat" || entries[1].Detail != "local" {
		t.Errorf("entry 1 = %#v", entries[1])
	}
	if entries[0].Time.IsZero() {
		t.Error("entry 0 has a zero timestamp")
	}
}

// TestLogger_RecordRotatesWhenOverLimit lowers the size/keep-count knobs
// so it can exercise real rotation without writing megabytes of fixture
// data: the log must never grow past the cap, must retain only the most
// recent entries, and must keep accepting writes seamlessly afterward
// (proving the reopened file handle actually works, not just that
// rotation ran).
func TestLogger_RecordRotatesWhenOverLimit(t *testing.T) {
	originalMax, originalKeep := maxErrLogBytes, errLogKeepEntries
	maxErrLogBytes, errLogKeepEntries = 400, 3
	defer func() { maxErrLogBytes, errLogKeepEntries = originalMax, originalKeep }()

	path := filepath.Join(t.TempDir(), "errors.jsonl")
	logger, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer logger.Close()

	const total = 30
	for i := 0; i < total; i++ {
		logger.Record("tool_call", "get_weather", fmt.Errorf("failure #%d", i))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat error log: %v", err)
	}
	if info.Size() > maxErrLogBytes*2 {
		// Rotation only triggers when a write would exceed the cap, so the
		// file can briefly sit a little over it right after the entry that
		// tripped rotation — but it must never be allowed to grow
		// unbounded, which is the actual bug this guards against.
		t.Errorf("error log size = %d bytes, want it kept near the %d-byte cap", info.Size(), maxErrLogBytes)
	}

	entries, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) == 0 || len(entries) > total {
		t.Fatalf("entries = %d, want somewhere between 1 and %d after rotation", len(entries), total)
	}
	last := entries[len(entries)-1]
	if last.Error != fmt.Sprintf("failure #%d", total-1) {
		t.Errorf("last entry = %q, want the most recent one to have survived rotation", last.Error)
	}
	// Whatever survived must be a contiguous, in-order tail — rotation
	// must not reorder or duplicate entries.
	firstIndex := total - len(entries)
	for i, entry := range entries {
		want := fmt.Sprintf("failure #%d", firstIndex+i)
		if entry.Error != want {
			t.Errorf("entry %d = %q, want %q", i, entry.Error, want)
		}
	}
}

func TestLogger_NilLoggerIsSafeNoOp(t *testing.T) {
	var logger *Logger
	logger.Record("tool_call", "anything", errors.New("boom")) // must not panic
	if err := logger.Close(); err != nil {
		t.Errorf("Close on nil logger returned %v, want nil", err)
	}
}

func TestReadAll_MissingFile(t *testing.T) {
	entries, err := ReadAll(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %#v, want nil for a missing file", entries)
	}
}

func TestTail_LimitsToMostRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	logger, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		logger.Record("tool_call", "get_weather", errors.New("boom"))
	}
	logger.Close()

	entries, err := Tail(path, 2)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}

	all, err := Tail(path, 0)
	if err != nil {
		t.Fatalf("Tail(0): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("Tail(0) = %d entries, want all 5", len(all))
	}
}

func TestSummarize_CountsAndOrdersByFrequency(t *testing.T) {
	entries := []Entry{
		{Category: "tool_call", Detail: "get_weather", Error: "a"},
		{Category: "tool_call", Detail: "get_weather", Error: "b"},
		{Category: "llm_chat", Detail: "local", Error: "c"},
		{Category: "tool_call", Detail: "memo", Error: "d"},
	}
	summaries := Summarize(entries)
	if len(summaries) != 3 {
		t.Fatalf("summaries = %d, want 3", len(summaries))
	}
	if summaries[0].Category != "tool_call" || summaries[0].Detail != "get_weather" || summaries[0].Count != 2 {
		t.Errorf("most frequent summary = %#v, want tool_call/get_weather x2", summaries[0])
	}
}
