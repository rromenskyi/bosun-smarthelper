package adventure

import (
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndLoadSession(t *testing.T) {
	s := openTestStore(t)

	game, err := s.CreateSession("alice", 12345)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if game.Loc == 0 {
		t.Fatal("expected a non-zero starting location after initial ProcessCommand")
	}

	loaded, err := s.LoadSession("alice")
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if loaded.Loc != game.Loc {
		t.Errorf("Loc: got %d, want %d", loaded.Loc, game.Loc)
	}
}

func TestCreateSessionDuplicateName(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("bob", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, err := s.CreateSession("bob", 2); err != ErrSessionExists {
		t.Errorf("expected ErrSessionExists, got %v", err)
	}
}

func TestLoadSessionNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.LoadSession("nobody"); err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestPlayPersistsStateAndHistory(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("carol", 42); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	output, _, _, gameOver, err := s.Play("carol", "look", "")
	if err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	if output == "" {
		t.Error("expected non-empty output from Play")
	}
	if gameOver {
		t.Error("game should not be over after a single look command")
	}

	output2, _, _, _, err := s.Play("carol", "inventory", "")
	if err != nil {
		t.Fatalf("second Play failed: %v", err)
	}
	if output2 == "" {
		t.Error("expected non-empty output from second Play")
	}

	history, err := s.History("carol", 0)
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}
	if history[0].Command != "look" || history[1].Command != "inventory" {
		t.Errorf("history out of order: %+v", history)
	}
}

// TestPlayThreadsLanguageThrough covers the actual point of Play's
// language parameter: it's applied fresh on every call, not persisted
// with the session, so the same session can move between languages
// turn to turn (matching a UI language toggle, which is exactly what
// drives this in production — see internal/webui/adventure.go).
func TestPlayThreadsLanguageThrough(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateSession("polina", 42); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// LOC_BUILDING (east from the start location) has Russian text —
	// see go-adventure's adventure.yaml.
	outputRu, _, _, _, err := s.Play("polina", "east", "ru")
	if err != nil {
		t.Fatalf("Play (ru) failed: %v", err)
	}
	if !strings.Contains(outputRu, "здания") {
		t.Errorf("Play with language=ru = %q, want it to contain translated Russian text", outputRu)
	}

	if _, _, _, _, err := s.Play("polina", "west", ""); err != nil {
		t.Fatalf("Play (back west) failed: %v", err)
	}

	outputEn, _, _, _, err := s.Play("polina", "east", "")
	if err != nil {
		t.Fatalf("Play (en) failed: %v", err)
	}
	if !strings.Contains(outputEn, "well house") {
		t.Errorf("Play with language=\"\" = %q, want the English text", outputEn)
	}
}

func TestPlayReportsLocationChange(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateSession("mover", 42); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// "look" never moves the player.
	_, _, changed, _, err := s.Play("mover", "look", "")
	if err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	if changed {
		t.Error("look should not report a location change")
	}
}

func TestPlaySubstitutesFallbackForEmptyEngineOutput(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateSession("empty1", 42); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Confirmed live: a single recognized noun with no verb (e.g. just
	// naming an object) leaves game.Output genuinely empty — a real
	// upstream go-adventure parser gap, not something to fix by reaching
	// into its dispatch logic here. Play must never hand the caller an
	// empty string regardless of which input triggers it.
	output, _, _, _, err := s.Play("empty1", "lamp", "")
	if err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	if output == "" {
		t.Fatal("Play returned an empty output — the fallback should have caught this")
	}
	if output != emptyOutputFallback {
		t.Errorf("output = %q, want the fallback %q", output, emptyOutputFallback)
	}

	// The fallback must also be what gets persisted to history — an
	// empty string there would be just as bad for any future UI reading
	// it back.
	history, err := s.History("empty1", 0)
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) != 1 || history[0].Output != emptyOutputFallback {
		t.Errorf("history = %+v, want one entry with the fallback output", history)
	}
}

func TestSessionIsolation(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("dave", 1); err != nil {
		t.Fatalf("CreateSession dave failed: %v", err)
	}
	if _, err := s.CreateSession("erin", 2); err != nil {
		t.Fatalf("CreateSession erin failed: %v", err)
	}

	if _, _, _, _, err := s.Play("dave", "north", ""); err != nil {
		t.Fatalf("Play dave failed: %v", err)
	}

	daveHistory, err := s.History("dave", 0)
	if err != nil {
		t.Fatalf("History dave failed: %v", err)
	}
	erinHistory, err := s.History("erin", 0)
	if err != nil {
		t.Fatalf("History erin failed: %v", err)
	}

	if len(daveHistory) != 1 {
		t.Errorf("dave: expected 1 history entry, got %d", len(daveHistory))
	}
	if len(erinHistory) != 0 {
		t.Errorf("erin: expected 0 history entries, got %d", len(erinHistory))
	}
}

func TestListSessions(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("frank", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, err := s.CreateSession("grace", 2); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	infos, err := s.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(infos))
	}
}

func TestDeleteSessionCascadesHistoryAndMemos(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("henry", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, _, _, _, err := s.Play("henry", "look", ""); err != nil {
		t.Fatalf("Play failed: %v", err)
	}
	if err := s.AddMemo("henry", "stuck near the grate"); err != nil {
		t.Fatalf("AddMemo failed: %v", err)
	}

	if err := s.DeleteSession("henry"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	if _, err := s.LoadSession("henry"); err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound after delete, got %v", err)
	}
	if _, err := s.History("henry", 0); err != ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for history after delete, got %v", err)
	}
}

func TestRenameSession(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("ivy", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := s.RenameSession("ivy", "jack"); err != nil {
		t.Fatalf("RenameSession failed: %v", err)
	}
	if _, err := s.LoadSession("jack"); err != nil {
		t.Errorf("expected renamed session to load, got %v", err)
	}
	if _, err := s.LoadSession("ivy"); err != ErrSessionNotFound {
		t.Errorf("expected old name to be gone, got %v", err)
	}
}

func TestMemos(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("kate", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := s.AddMemo("kate", "first note"); err != nil {
		t.Fatalf("AddMemo failed: %v", err)
	}
	if err := s.AddMemo("kate", "second note"); err != nil {
		t.Fatalf("AddMemo failed: %v", err)
	}

	memos, err := s.Memos("kate")
	if err != nil {
		t.Fatalf("Memos failed: %v", err)
	}
	if len(memos) != 2 {
		t.Fatalf("expected 2 memos, got %d", len(memos))
	}
	if memos[0].Content != "first note" || memos[1].Content != "second note" {
		t.Errorf("memos out of order: %+v", memos)
	}
}

func TestActiveSession(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("liam", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if _, ok, err := s.ActiveSession("chat-1"); err != nil || ok {
		t.Fatalf("expected no active session initially, got ok=%v err=%v", ok, err)
	}

	if err := s.SetActiveSession("chat-1", "liam"); err != nil {
		t.Fatalf("SetActiveSession failed: %v", err)
	}

	name, ok, err := s.ActiveSession("chat-1")
	if err != nil || !ok || name != "liam" {
		t.Fatalf("expected active session liam, got name=%q ok=%v err=%v", name, ok, err)
	}

	// Setting again for the same chat session overwrites, not duplicates.
	if _, err := s.CreateSession("mia", 2); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := s.SetActiveSession("chat-1", "mia"); err != nil {
		t.Fatalf("SetActiveSession overwrite failed: %v", err)
	}
	name, ok, err = s.ActiveSession("chat-1")
	if err != nil || !ok || name != "mia" {
		t.Fatalf("expected active session mia after overwrite, got name=%q ok=%v err=%v", name, ok, err)
	}
}

func TestDeleteSessionClearsActivePointer(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateSession("noah", 1); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := s.SetActiveSession("chat-2", "noah"); err != nil {
		t.Fatalf("SetActiveSession failed: %v", err)
	}
	if err := s.DeleteSession("noah"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	if _, ok, err := s.ActiveSession("chat-2"); err != nil || ok {
		t.Fatalf("expected active pointer cleared after delete, got ok=%v err=%v", ok, err)
	}
}
