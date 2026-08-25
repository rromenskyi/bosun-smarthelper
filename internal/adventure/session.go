package adventure

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/rromenskyi/go-adventure/advent"
)

// ErrSessionNotFound is returned when a named session does not exist.
var ErrSessionNotFound = errors.New("adventure session not found")

// ErrSessionExists is returned by CreateSession when the name is taken.
var ErrSessionExists = errors.New("adventure session already exists")

// SessionInfo is a lightweight summary of a session, suitable for
// listing without deserializing the full game state.
type SessionInfo struct {
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Turns      int
	LocationID int32
	GameOver   bool
}

// CreateSession starts a new game under name and persists its initial
// state. seed of 0 picks a random one.
func (s *Store) CreateSession(name string, seed int) (*advent.Game, error) {
	if seed == 0 {
		seed = int(rand.Int31())
	}

	game := advent.NewGame(seed, "", "", "", false, false, false, nil)
	if err := game.ProcessCommand(""); err != nil {
		return nil, fmt.Errorf("initialize game: %w", err)
	}

	data, err := game.SaveToBytes()
	if err != nil {
		return nil, fmt.Errorf("serialize initial state: %w", err)
	}

	now := time.Now()
	_, err = s.db.Exec(
		`INSERT INTO sessions (name, created_at, updated_at, state, turns, location_id, game_over)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, now, now, data, game.Turns, game.Loc, game.GameOver,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrSessionExists
		}
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return &game, nil
}

// ListSessions returns a summary of every session, most recently
// updated first.
func (s *Store) ListSessions() ([]SessionInfo, error) {
	rows, err := s.db.Query(
		`SELECT name, created_at, updated_at, turns, location_id, game_over
		 FROM sessions ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var infos []SessionInfo
	for rows.Next() {
		var info SessionInfo
		if err := rows.Scan(&info.Name, &info.CreatedAt, &info.UpdatedAt,
			&info.Turns, &info.LocationID, &info.GameOver); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		infos = append(infos, info)
	}
	return infos, rows.Err()
}

// LoadSession deserializes and returns the current game state for name.
func (s *Store) LoadSession(name string) (*advent.Game, error) {
	var data []byte
	err := s.db.QueryRow(`SELECT state FROM sessions WHERE name = ?`, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}

	game := &advent.Game{}
	if err := game.LoadFromBytes(data); err != nil {
		return nil, fmt.Errorf("deserialize state: %w", err)
	}
	return game, nil
}

// SaveSession persists g as the current state of session name.
func (s *Store) SaveSession(name string, g *advent.Game) error {
	data, err := g.SaveToBytes()
	if err != nil {
		return fmt.Errorf("serialize state: %w", err)
	}

	res, err := s.db.Exec(
		`UPDATE sessions SET state = ?, updated_at = ?, turns = ?, location_id = ?, game_over = ?
		 WHERE name = ?`,
		data, time.Now(), g.Turns, g.Loc, g.GameOver, name,
	)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return checkAffected(res, ErrSessionNotFound)
}

// DeleteSession removes a session and its history/memos (cascade).
func (s *Store) DeleteSession(name string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if err := checkAffected(res, ErrSessionNotFound); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM active_sessions WHERE session_name = ?`, name)
	if err != nil {
		return fmt.Errorf("clear active-session pointers: %w", err)
	}
	return nil
}

// RenameSession renames a session in place.
func (s *Store) RenameSession(oldName, newName string) error {
	res, err := s.db.Exec(`UPDATE sessions SET name = ? WHERE name = ?`, newName, oldName)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ErrSessionExists
		}
		return fmt.Errorf("rename session: %w", err)
	}
	if err := checkAffected(res, ErrSessionNotFound); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE active_sessions SET session_name = ? WHERE session_name = ?`, newName, oldName)
	if err != nil {
		return fmt.Errorf("update active-session pointers: %w", err)
	}
	return nil
}

// emptyOutputFallback stands in for a genuinely empty game.Output — this
// happens for real, reproducibly, on some inputs the engine's parser
// accepts but has no message for (confirmed live, e.g. a bare object
// name with no verb, such as "lamp"). That's an upstream go-adventure
// gap, not something to patch here by reaching into its parser — this
// integration's own responsibility is simply to never hand the user an
// empty reply. "Nothing happens." is the engine's own idiom for exactly
// this case (it's what a recognized-but-inert command like "xyzzy"
// already answers with).
const emptyOutputFallback = "Nothing happens."

// Play loads session name, applies command, appends the turn to its
// history, persists the resulting state, and returns the game's output
// text, current location, whether the location changed this turn (so a
// caller can decide whether to refresh art/ambient audio), and whether
// the game has ended.
// Play processes one command against the named session. language selects
// which of a location's descriptions the engine renders ("ru" for
// Russian, anything else — including "" — for English); it is not
// persisted as part of the session, it's applied fresh from the
// caller's own current language preference on every single turn, the
// same way narration language is already chosen per turn rather than
// stored in the save file. See go-adventure's advent.Game.Language.
func (s *Store) Play(name, command, language string) (output string, locationID int32, locationChanged bool, gameOver bool, err error) {
	game, err := s.LoadSession(name)
	if err != nil {
		return "", 0, false, false, err
	}
	game.Language = language
	previousLoc := game.Loc

	if err := game.ProcessCommand(command); err != nil {
		return "", 0, false, false, fmt.Errorf("process command: %w", err)
	}
	if game.Output == "" {
		game.Output = emptyOutputFallback
	}

	if err := s.AppendHistory(name, int(game.Turns), command, game.Output); err != nil {
		return "", 0, false, false, fmt.Errorf("append history: %w", err)
	}

	if err := s.SaveSession(name, game); err != nil {
		return "", 0, false, false, err
	}

	return game.Output, game.Loc, game.Loc != previousLoc, game.GameOver, nil
}

// SetActiveSession records which named adventure session a chat
// conversation (chatSessionID) is currently pointed at.
func (s *Store) SetActiveSession(chatSessionID, sessionName string) error {
	_, err := s.db.Exec(
		`INSERT INTO active_sessions (chat_session_id, session_name) VALUES (?, ?)
		 ON CONFLICT(chat_session_id) DO UPDATE SET session_name = excluded.session_name`,
		chatSessionID, sessionName,
	)
	if err != nil {
		return fmt.Errorf("set active session: %w", err)
	}
	return nil
}

// ActiveSession returns the named adventure session chatSessionID last
// selected, if any.
func (s *Store) ActiveSession(chatSessionID string) (string, bool, error) {
	var name string
	err := s.db.QueryRow(
		`SELECT session_name FROM active_sessions WHERE chat_session_id = ?`, chatSessionID,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query active session: %w", err)
	}
	return name, true, nil
}

func checkAffected(res sql.Result, notFoundErr error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return notFoundErr
	}
	return nil
}

func isUniqueConstraintErr(err error) bool {
	// modernc.org/sqlite wraps the underlying SQLite error message and
	// does not expose a typed constraint-violation error, so matching
	// on the message is the documented approach.
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
