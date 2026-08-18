package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

// MemoTool stores and retrieves dated notes in a local JSON file.
type MemoTool struct {
	path string
	mu   sync.Mutex
}

type memoRecord struct {
	Key        string `json:"key"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	ArchivedAt string `json:"archived_at,omitempty"`
}

type memoFile struct {
	Memos map[string]memoRecord `json:"memos"`
}

// NewMemoTool creates a persistent local memo tool.
func NewMemoTool(cfg *config.MemoConfig) *MemoTool {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".local", "share", "bosun", "memos.json")
		} else {
			path = "memos.json"
		}
	}
	return &MemoTool{path: path}
}

func (t *MemoTool) Name() string {
	return "memo"
}

func (t *MemoTool) Description() string {
	return "Write, read, list, archive, or delete persistent local memos. Listing exposes timestamps, status, and age_days so old notes can be reviewed."
}

func (t *MemoTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"write", "read", "list", "archive", "delete"},
				"description": "Operation to perform. Use list to inspect memo dates and age before archival or deletion.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Short stable memo identifier; required except for list.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Memo text; required for write and omitted for read.",
			},
			"include_archived": map[string]any{
				"type":        "boolean",
				"description": "For list, include archived memos as well as active memos.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func (t *MemoTool) Execute(_ context.Context, args map[string]any) (any, error) {
	action, _ := args["action"].(string)
	key, _ := args["key"].(string)
	action = strings.TrimSpace(action)
	key = strings.TrimSpace(key)
	if action != "list" && (key == "" || len([]rune(key)) > 128) {
		return nil, fmt.Errorf("memo key must contain 1 to 128 characters")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := t.load()
	if err != nil {
		return nil, err
	}

	switch action {
	case "read":
		record, ok := data.Memos[key]
		if !ok {
			return nil, fmt.Errorf("memo %q was not found", key)
		}
		return memoView(record, time.Now()), nil
	case "list":
		includeArchived, _ := args["include_archived"].(bool)
		now := time.Now()
		records := make([]memoRecord, 0, len(data.Memos))
		for _, record := range data.Memos {
			if record.Status == "archived" && !includeArchived {
				continue
			}
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].UpdatedAt < records[j].UpdatedAt
		})
		views := make([]map[string]any, 0, len(records))
		for _, record := range records {
			views = append(views, memoView(record, now))
		}
		return map[string]any{"memos": views, "count": len(views)}, nil
	case "write":
		content, _ := args["content"].(string)
		content = strings.TrimSpace(content)
		if content == "" {
			return nil, fmt.Errorf("memo content is required for write")
		}
		if len([]rune(content)) > 10000 {
			return nil, fmt.Errorf("memo content must not exceed 10000 characters")
		}
		now := time.Now().Format(time.RFC3339)
		record, exists := data.Memos[key]
		if !exists {
			record = memoRecord{Key: key, CreatedAt: now}
		}
		record.Content = content
		record.Status = "active"
		record.UpdatedAt = now
		record.ArchivedAt = ""
		data.Memos[key] = record
		if err := t.save(data); err != nil {
			return nil, err
		}
		return memoView(record, time.Now()), nil
	case "archive":
		record, ok := data.Memos[key]
		if !ok {
			return nil, fmt.Errorf("memo %q was not found", key)
		}
		now := time.Now().Format(time.RFC3339)
		record.Status = "archived"
		record.ArchivedAt = now
		record.UpdatedAt = now
		data.Memos[key] = record
		if err := t.save(data); err != nil {
			return nil, err
		}
		return memoView(record, time.Now()), nil
	case "delete":
		record, ok := data.Memos[key]
		if !ok {
			return nil, fmt.Errorf("memo %q was not found", key)
		}
		delete(data.Memos, key)
		if err := t.save(data); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "memo": memoView(record, time.Now())}, nil
	default:
		return nil, fmt.Errorf("unsupported memo action %q", action)
	}
}

func memoView(record memoRecord, now time.Time) map[string]any {
	status := record.Status
	if status == "" {
		status = "active"
	}
	view := map[string]any{
		"key":        record.Key,
		"content":    record.Content,
		"status":     status,
		"created_at": record.CreatedAt,
		"updated_at": record.UpdatedAt,
		"age_days":   memoAgeDays(record.UpdatedAt, now),
	}
	if record.ArchivedAt != "" {
		view["archived_at"] = record.ArchivedAt
	}
	return view
}

func memoAgeDays(updatedAt string, now time.Time) int {
	updated, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil || now.Before(updated) {
		return 0
	}
	return int(now.Sub(updated).Hours() / 24)
}

func (t *MemoTool) load() (memoFile, error) {
	data := memoFile{Memos: make(map[string]memoRecord)}
	file, err := os.Open(t.path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("open memo store: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return data, fmt.Errorf("decode memo store: %w", err)
	}
	if data.Memos == nil {
		data.Memos = make(map[string]memoRecord)
	}
	return data, nil
}

func (t *MemoTool) save(data memoFile) error {
	directory := filepath.Dir(t.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create memo directory: %w", err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode memo store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".memos-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary memo store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set memo store permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write memo store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync memo store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close memo store: %w", err)
	}
	if err := os.Rename(temporaryPath, t.path); err != nil {
		return fmt.Errorf("replace memo store: %w", err)
	}
	return nil
}
