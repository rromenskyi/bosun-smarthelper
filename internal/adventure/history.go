package adventure

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// HistoryEntry is one recorded turn. History is informational only —
// never read back to reconstruct game state, which always comes from a
// session's own serialized state (see session.go).
type HistoryEntry struct {
	Turn      int
	Command   string
	Output    string
	CreatedAt time.Time
}

// AppendHistory records one turn for session name.
func (s *Store) AppendHistory(name string, turn int, command, output string) error {
	id, err := s.sessionID(name)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`INSERT INTO history (session_id, turn, command, output, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, turn, command, output, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("insert history: %w", err)
	}
	return nil
}

// History returns the most recent limit turns for session name, oldest
// first. limit <= 0 returns the full history.
func (s *Store) History(name string, limit int) ([]HistoryEntry, error) {
	id, err := s.sessionID(name)
	if err != nil {
		return nil, err
	}

	query := `SELECT turn, command, output, created_at FROM history WHERE session_id = ? ORDER BY turn DESC`
	args := []any{id}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Turn, &e.Command, &e.Output, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Rows came back newest-first (for LIMIT to keep the most recent
	// ones); reverse so callers get chronological order.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
}

func (s *Store) sessionID(name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM sessions WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSessionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("look up session: %w", err)
	}
	return id, nil
}
