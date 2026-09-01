// Package notifications persists alerts (internal/alerts) somewhere a
// user can actually come back to later — a NOAA warning spoken once
// through the speaker, or a threshold crossing sent to Telegram, is
// otherwise gone the moment it's delivered: nothing keeps a record of it
// inside the app itself. This is that record, read by the web UI's
// notification zone (a bell/badge in the header, not a system-prompt
// concern — the model never sees these).
package notifications

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxNotifications caps how many are kept, oldest dropped first — same
// rotation reasoning as internal/errlog: enough recent history to be
// useful without growing forever on a long-lived deployment.
const maxNotifications = 200

// Notification is one persisted alert.
type Notification struct {
	ID       string    `json:"id"`
	Source   string    `json:"source"` // "noaa" or "threshold" — see alerts.Alert.Source
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	At       time.Time `json:"at"`
	Read     bool      `json:"read"`
}

type storeFile struct {
	Notifications []Notification `json:"notifications"`
}

// Store persists notifications to a local JSON file, atomically — same
// pattern as internal/documents/store.go's save.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a notification store. Empty path resolves to the same
// default data directory every other store falls back to.
func NewStore(path string) *Store {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".local", "share", "bosun", "notifications.json")
		} else {
			path = "notifications.json"
		}
	}
	return &Store{path: path}
}

// Add records a new notification, generating its ID and defaulting At to
// now if unset. Trims the oldest entries once the store exceeds
// maxNotifications.
func (s *Store) Add(n Notification) (Notification, error) {
	if n.At.IsZero() {
		n.At = time.Now()
	}
	if n.ID == "" {
		id, err := newID()
		if err != nil {
			return Notification{}, fmt.Errorf("generate notification id: %w", err)
		}
		n.ID = id
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return Notification{}, err
	}
	data.Notifications = append(data.Notifications, n)
	if len(data.Notifications) > maxNotifications {
		data.Notifications = data.Notifications[len(data.Notifications)-maxNotifications:]
	}
	if err := s.save(data); err != nil {
		return Notification{}, err
	}
	return n, nil
}

// List returns every notification, most recent first.
func (s *Store) List() ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Notification, len(data.Notifications))
	copy(out, data.Notifications)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// UnreadCount is List filtered to Read == false, without decoding twice
// from the caller's perspective — used for the header badge.
func (s *Store) UnreadCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, n := range data.Notifications {
		if !n.Read {
			count++
		}
	}
	return count, nil
}

// MarkRead sets Read on one notification by ID. Not an error if id
// doesn't exist — a dismiss-then-mark-read race from two browser tabs
// shouldn't surface as a failure to either.
func (s *Store) MarkRead(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	for i := range data.Notifications {
		if data.Notifications[i].ID == id {
			data.Notifications[i].Read = true
			break
		}
	}
	return s.save(data)
}

// MarkAllRead sets Read on every notification.
func (s *Store) MarkAllRead() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	for i := range data.Notifications {
		data.Notifications[i].Read = true
	}
	return s.save(data)
}

// Delete removes one notification by ID. Not an error if it doesn't
// exist — dismissing something already gone is a no-op, not a failure.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load()
	if err != nil {
		return err
	}
	kept := make([]Notification, 0, len(data.Notifications))
	for _, n := range data.Notifications {
		if n.ID != id {
			kept = append(kept, n)
		}
	}
	data.Notifications = kept
	return s.save(data)
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) load() (storeFile, error) {
	data := storeFile{}
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("open notification store: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return data, fmt.Errorf("decode notification store: %w", err)
	}
	return data, nil
}

func (s *Store) save(data storeFile) error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create notification directory: %w", err)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode notification store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".notifications-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary notification store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set notification store permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write notification store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync notification store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close notification store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace notification store: %w", err)
	}
	return nil
}
