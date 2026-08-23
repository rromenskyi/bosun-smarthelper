package adventure

import (
	"fmt"
	"time"
)

// Memo is one entry in a session's game-memo scratchpad — a place for
// a future narration/voice layer to jot free-form notes about a
// session (e.g. "player seems stuck near the grate"). Append-only, one
// session can have many. This is unrelated to and never touches
// Старпом's own memo/notes feature.
type Memo struct {
	Content   string
	CreatedAt time.Time
}

// AddMemo appends a memo entry to session name.
func (s *Store) AddMemo(name, content string) error {
	id, err := s.sessionID(name)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`INSERT INTO memos (session_id, content, created_at) VALUES (?, ?, ?)`,
		id, content, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("insert memo: %w", err)
	}
	return nil
}

// Memos returns every memo for session name, oldest first.
func (s *Store) Memos(name string) ([]Memo, error) {
	id, err := s.sessionID(name)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT content, created_at FROM memos WHERE session_id = ? ORDER BY created_at ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("query memos: %w", err)
	}
	defer rows.Close()

	var memos []Memo
	for rows.Next() {
		var m Memo
		if err := rows.Scan(&m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan memo: %w", err)
		}
		memos = append(memos, m)
	}
	return memos, rows.Err()
}
