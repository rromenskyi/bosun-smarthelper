// Package settings persists user-editable runtime settings (assistant
// persona/style prompt, default language, LLM temperatures, memo tag
// canonicalization vocabulary) that started life as config.yaml defaults
// but can be tweaked live from the web UI's settings page without a
// restart — see docs/settings.md.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Data is the editable settings blob. Once a Store's file exists, it is
// the source of truth — config.yaml's values only seed it the very first
// time (see Load).
type Data struct {
	NameRU            string   `json:"name_ru,omitempty"`
	NameEN            string   `json:"name_en,omitempty"`
	StylePrompt       string   `json:"style_prompt"`
	DefaultLanguage   string   `json:"default_language,omitempty"`
	RemoteTemperature float64  `json:"remote_temperature"`
	LocalTemperature  float64  `json:"local_temperature"`
	CanonicalTags     []string `json:"canonical_tags,omitempty"`
	// BackupAutoEnabled/BackupIntervalHours turn on the otherwise-manual-only
	// `smarthelper backup` (docs/backup.md) as a background schedule — off
	// by default, same as every other opt-in background pass in this
	// project. Meaningless without backup.s3 configured in config.yaml;
	// see webui's "backup_configured" status field.
	BackupAutoEnabled   bool `json:"backup_auto_enabled,omitempty"`
	BackupIntervalHours int  `json:"backup_interval_hours,omitempty"`
	// AlertsXEnabled toggles a channel that's already configured
	// (config.yaml's alerts.channels.*, docs/alerts.md) — off by default;
	// meaningless (and hidden by the settings page) for a channel that
	// isn't configured at all.
	AlertsTelegramEnabled bool `json:"alerts_telegram_enabled,omitempty"`
	AlertsWebhookEnabled  bool `json:"alerts_webhook_enabled,omitempty"`
	AlertsSpeakerEnabled  bool `json:"alerts_speaker_enabled,omitempty"`
}

func (d *Data) normalize() {
	d.NameRU = strings.TrimSpace(d.NameRU)
	d.NameEN = strings.TrimSpace(d.NameEN)
	d.StylePrompt = strings.TrimSpace(d.StylePrompt)
	d.DefaultLanguage = strings.TrimSpace(d.DefaultLanguage)
	tags := make([]string, 0, len(d.CanonicalTags))
	for _, tag := range d.CanonicalTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	d.CanonicalTags = tags
}

// Store persists Data atomically, the same pattern as memo/documents
// stores (internal/tools/memo.go, internal/documents/store.go).
type Store struct {
	path string
	mu   sync.RWMutex
	data Data
}

// DefaultPath is ~/.local/share/bosun/settings.json, the same base
// directory memos/documents/sessions use by default.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "settings.json"
	}
	return filepath.Join(home, ".local", "share", "bosun", "settings.json")
}

// Load reads path if it exists; otherwise it starts from defaults
// (typically config.yaml's current values at process startup) and
// persists that as the initial file, so config.yaml only ever seeds a
// fresh install — a later restart picks up whatever was last saved here,
// not config.yaml again.
func Load(path string, defaults Data) (*Store, error) {
	defaults.normalize()
	store := &Store{path: path, data: defaults}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return store, store.save()
	}
	if err != nil {
		return nil, fmt.Errorf("open settings store: %w", err)
	}
	defer file.Close()
	var data Data
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode settings store: %w", err)
	}
	data.normalize()
	store.data = data
	return store, nil
}

// Get returns the current settings.
func (s *Store) Get() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// Update replaces the stored settings and persists them.
func (s *Store) Update(data Data) error {
	data.normalize()
	s.mu.Lock()
	s.data = data
	s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	s.mu.RLock()
	data := s.data
	s.mu.RUnlock()

	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set settings store permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write settings store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync settings store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close settings store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace settings store: %w", err)
	}
	return nil
}
