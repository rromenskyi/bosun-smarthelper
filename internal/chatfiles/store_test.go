package chatfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "chatfiles"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestSaveAndList(t *testing.T) {
	store := newTestStore(t)

	if _, err := store.Save("session-1", "photo.jpg", strings.NewReader("fake-jpeg-bytes")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Save("session-1", "notes.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	files, err := store.List("session-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %#v, want 2", files)
	}
	if files[0].Name != "notes.txt" || files[1].Name != "photo.jpg" {
		t.Errorf("files = %#v, want alphabetical notes.txt, photo.jpg", files)
	}
	if files[1].Size != int64(len("fake-jpeg-bytes")) {
		t.Errorf("photo.jpg size = %d, want %d", files[1].Size, len("fake-jpeg-bytes"))
	}
}

func TestListUnknownSessionReturnsEmptyNotError(t *testing.T) {
	store := newTestStore(t)
	files, err := store.List("never-existed")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %#v, want empty", files)
	}
}

func TestSessionsAreIsolated(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save("session-a", "shared-name.txt", strings.NewReader("from a")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Save("session-b", "shared-name.txt", strings.NewReader("from b")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	contentA, err := store.Read("session-a", "shared-name.txt")
	if err != nil {
		t.Fatalf("Read a: %v", err)
	}
	if string(contentA) != "from a" {
		t.Errorf("session-a content = %q, want %q", contentA, "from a")
	}

	contentB, err := store.Read("session-b", "shared-name.txt")
	if err != nil {
		t.Fatalf("Read b: %v", err)
	}
	if string(contentB) != "from b" {
		t.Errorf("session-b content = %q, want %q", contentB, "from b")
	}
}

func TestSaveOverwritesSameName(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save("session-1", "notes.txt", strings.NewReader("first")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Save("session-1", "notes.txt", strings.NewReader("second")); err != nil {
		t.Fatalf("Save (overwrite): %v", err)
	}
	content, err := store.Read("session-1", "notes.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(content) != "second" {
		t.Errorf("content = %q, want %q (overwrite, not append)", content, "second")
	}
	files, err := store.List("session-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("files = %#v, want exactly 1 (overwrite must not create a duplicate)", files)
	}
}

func TestSaveEnforcesMaxFilesPerSession(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < maxFilesPerSession; i++ {
		name := "file" + string(rune('a'+i)) + ".txt"
		if _, err := store.Save("session-1", name, strings.NewReader("x")); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	if _, err := store.Save("session-1", "one-too-many.txt", strings.NewReader("x")); err == nil {
		t.Error("expected an error once the session is at its file limit")
	}
}

func TestSaveRejectsPathTraversalInFilename(t *testing.T) {
	store := newTestStore(t)
	// filepath.Base strips any directory component, so this should land
	// as a plain "etc-passwd"-ish basename inside the session dir, never
	// escaping it.
	name, err := store.Save("session-1", "../../etc/passwd", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if strings.Contains(name, "..") || strings.ContainsRune(name, os.PathSeparator) {
		t.Errorf("saved name = %q, want no path traversal characters", name)
	}
}

func TestSaveRejectsInvalidSessionID(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save("../escape", "file.txt", strings.NewReader("x")); err == nil {
		t.Error("expected an error for a session id containing path separators")
	}
}

func TestForgetRemovesFile(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save("session-1", "notes.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Forget("session-1", "notes.txt"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	files, err := store.List("session-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %#v, want empty after Forget", files)
	}
	// Forgetting an already-gone file is not an error.
	if err := store.Forget("session-1", "notes.txt"); err != nil {
		t.Errorf("Forget (already gone): %v", err)
	}
}

func TestReapRemovesOnlyExpiredSessions(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save("old-session", "notes.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Save("fresh-session", "notes.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.root, "old-session"), oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if err := store.Reap(1 * time.Hour); err != nil {
		t.Fatalf("Reap: %v", err)
	}

	oldFiles, err := store.List("old-session")
	if err != nil {
		t.Fatalf("List old-session: %v", err)
	}
	if len(oldFiles) != 0 {
		t.Errorf("old-session files = %#v, want reaped away", oldFiles)
	}
	freshFiles, err := store.List("fresh-session")
	if err != nil {
		t.Fatalf("List fresh-session: %v", err)
	}
	if len(freshFiles) != 1 {
		t.Errorf("fresh-session files = %#v, want untouched", freshFiles)
	}
}
