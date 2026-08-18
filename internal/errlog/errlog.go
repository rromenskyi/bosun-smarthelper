// Package errlog collects operational failures — a tool call that errored,
// an LLM request that failed — into one append-only, file-backed feed. It's
// not a general-purpose logger: slog already covers that. This is meant to
// answer one question later: "what keeps failing, and is it worth fixing?"
package errlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one recorded failure.
type Entry struct {
	Time     time.Time `json:"time"`
	Category string    `json:"category"` // e.g. "tool_call", "llm_chat"
	Detail   string    `json:"detail"`   // tool name, provider, etc.
	Error    string    `json:"error"`
}

// Logger appends Entry records to a JSONL file. A nil *Logger is a valid,
// safe no-op — callers that don't want error logging can simply not
// construct one and call methods on the nil value.
type Logger struct {
	mu   sync.Mutex
	file *os.File
}

// DefaultPath returns the default error log location, mirroring the memo
// store's convention (~/.local/share/bosun/...).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "errors.jsonl"
	}
	return filepath.Join(home, ".local", "share", "bosun", "errors.jsonl")
}

// Open opens (creating if needed) the error log at path. An empty path
// resolves to DefaultPath().
func Open(path string) (*Logger, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create error log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open error log: %w", err)
	}
	return &Logger{file: file}, nil
}

// Record appends a failure. A nil err or a nil Logger makes this a no-op,
// so callers never need to guard the call themselves.
func (l *Logger) Record(category, detail string, err error) {
	if l == nil || err == nil {
		return
	}
	payload, marshalErr := json.Marshal(Entry{
		Time:     time.Now(),
		Category: category,
		Detail:   detail,
		Error:    err.Error(),
	})
	if marshalErr != nil {
		return
	}
	payload = append(payload, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(payload)
}

// Close closes the underlying file. Safe to call on a nil Logger.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	return l.file.Close()
}

// ReadAll reads every entry from path in file order. A missing file yields
// no entries and no error.
func ReadAll(path string) ([]Entry, error) {
	if path == "" {
		path = DefaultPath()
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open error log: %w", err)
	}
	defer file.Close()

	var entries []Entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip a malformed line rather than fail the whole read
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read error log: %w", err)
	}
	return entries, nil
}

// Tail returns up to the last n entries from path, oldest first.
func Tail(path string, n int) ([]Entry, error) {
	entries, err := ReadAll(path)
	if err != nil {
		return nil, err
	}
	if n <= 0 || len(entries) <= n {
		return entries, nil
	}
	return entries[len(entries)-n:], nil
}

// Summary is a count of failures sharing a category and detail.
type Summary struct {
	Category string
	Detail   string
	Count    int
}

// Summarize groups entries by (category, detail), most frequent first.
func Summarize(entries []Entry) []Summary {
	counts := make(map[[2]string]int)
	for _, entry := range entries {
		counts[[2]string{entry.Category, entry.Detail}]++
	}
	summaries := make([]Summary, 0, len(counts))
	for key, count := range counts {
		summaries = append(summaries, Summary{Category: key[0], Detail: key[1], Count: count})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Count != summaries[j].Count {
			return summaries[i].Count > summaries[j].Count
		}
		if summaries[i].Category != summaries[j].Category {
			return summaries[i].Category < summaries[j].Category
		}
		return summaries[i].Detail < summaries[j].Detail
	})
	return summaries
}
