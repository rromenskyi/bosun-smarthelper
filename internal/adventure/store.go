// Package adventure integrates the go-adventure text-adventure engine
// (github.com/rromenskyi/go-adventure) into Старпом: named, durable game
// sessions backed by SQLite, exposed to the LLM as a tool and (later)
// through a chat "game mode". Optional end to end — see config.yaml's
// adventure.enabled, off by default, and docs/adventure.md.
package adventure

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DefaultPath returns the default adventure store location, mirroring
// internal/metrics.DefaultPath's convention (~/.local/share/bosun/...).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "adventure.db"
	}
	return filepath.Join(home, ".local", "share", "bosun", "adventure.db")
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	created_at  TIMESTAMP NOT NULL,
	updated_at  TIMESTAMP NOT NULL,
	state       BLOB NOT NULL,
	turns       INTEGER NOT NULL DEFAULT 0,
	location_id INTEGER NOT NULL DEFAULT 0,
	game_over   BOOLEAN NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS history (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	turn       INTEGER NOT NULL,
	command    TEXT NOT NULL,
	output     TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS memos (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	content    TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL
);

-- Which named adventure session a given chat conversation is currently
-- pointed at, so the LLM/game-mode doesn't have to keep repeating the
-- session name every turn once one is selected.
CREATE TABLE IF NOT EXISTS active_sessions (
	chat_session_id TEXT PRIMARY KEY,
	session_name    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_history_session ON history(session_id, turn);
CREATE INDEX IF NOT EXISTS idx_memos_session ON memos(session_id, created_at);
`

// Store is a SQLite-backed collection of named go-adventure game
// sessions. History is informational only (debugging/future UI), never
// authoritative — state always comes from a session's own serialized
// game state. Memos are a per-session, append-only scratchpad for a
// future narration layer's own notes; this is a separate system from
// Старпом's own memo/notes feature and never touches it.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at path, creating the
// schema (and any missing parent directory) if needed. An empty path
// resolves to DefaultPath().
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create adventure store directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open adventure store: %w", err)
	}

	// A single connection avoids SQLITE_BUSY from Go's pool writing
	// concurrently against itself — same reasoning as metrics.Store.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
