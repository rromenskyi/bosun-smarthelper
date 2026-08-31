// Package settings persists user-editable runtime settings (assistant
// persona/style prompt, default language, LLM temperatures, memo tag
// canonicalization vocabulary) that started life as config.yaml defaults
// but can be tweaked live from the web UI's settings page without a
// restart — see docs/settings.md.
package settings

import (
	"crypto/rand"
	"encoding/hex"
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
	// AlertsThresholds are web-managed metric threshold rules (see
	// docs/alerts.md) — config.yaml's alerts.thresholds only ever seeds
	// this list once (see main.go); after that this is authoritative,
	// added/edited/removed entirely from the settings page. Unlike
	// AlertsXEnabled above (which gate NOAA weather alerts, the one
	// global source), each rule here picks its own channels — there's no
	// single "enabled" toggle for thresholds as a whole.
	AlertsThresholds []AlertsThresholdRule `json:"alerts_thresholds,omitempty"`
	// DynamicTopicsEnabled, unlike every other toggle in this struct, is
	// seeded true by main.go for a brand-new settings store — the whole
	// point (nudging the model to check local filedump uploads before
	// guessing or reaching for web_search) is only useful when uploads
	// exist, but harmless when they don't, so on-by-default is the safer
	// failure mode rather than a feature nobody notices they need to flip
	// on. See internal/agent.Agent.SetDynamicTopicsEnabled.
	DynamicTopicsEnabled bool `json:"dynamic_topics_enabled,omitempty"`
}

// AlertsThresholdRule is one web-managed metric threshold — see
// docs/alerts.md. A channel checkbox only does something if that channel
// is also configured in config.yaml/.env (config decides what channels
// exist at all; this decides which of them this one rule uses).
type AlertsThresholdRule struct {
	ID       string  `json:"id"`
	Metric   string  `json:"metric"`
	Operator string  `json:"operator"`
	Value    float64 `json:"value"`
	Title    string  `json:"title,omitempty"`
	// SmoothingSamples > 1 compares a moving average of the last N raw
	// samples instead of the single latest reading — reduces false
	// alarms from a noisy sensor. <= 1 means no smoothing.
	SmoothingSamples int  `json:"smoothing_samples,omitempty"`
	Telegram         bool `json:"telegram,omitempty"`
	Webhook          bool `json:"webhook,omitempty"`
	Speaker          bool `json:"speaker,omitempty"`
	// Siren only means something alongside Speaker — plays a short
	// built-in sound before the spoken alert.
	Siren bool `json:"siren,omitempty"`
	// CustomText, if set, replaces the auto-generated alarm message sent
	// to every channel this rule has enabled (never the "back to normal"
	// message) — see internal/alerts.Threshold.CustomText.
	CustomText string `json:"custom_text,omitempty"`
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

	for i := range d.AlertsThresholds {
		rule := &d.AlertsThresholds[i]
		rule.Metric = strings.TrimSpace(rule.Metric)
		rule.Title = strings.TrimSpace(rule.Title)
		rule.CustomText = strings.TrimSpace(rule.CustomText)
		if rule.SmoothingSamples < 1 {
			rule.SmoothingSamples = 1
		}
		if rule.ID == "" {
			rule.ID = randomID()
		}
	}
}

// randomID generates a short opaque identifier for a threshold rule
// created either by seeding from config.yaml or by the settings page —
// crypto/rand + hex, the same idea as internal/webui's own
// newSessionID, kept package-local since settings doesn't import webui.
func randomID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand failing is effectively unrecoverable on any real
		// system; a fixed fallback still keeps normalize() from panicking,
		// worst case two rules briefly share an ID until the process
		// (and its broken entropy source) is fixed.
		return "fallback-id"
	}
	return hex.EncodeToString(buffer)
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
