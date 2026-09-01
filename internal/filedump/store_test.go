package filedump

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "filedump")
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// TestNewStoreReconcilesStalePendingFromPreviousProcess simulates a
// restart mid-ingestion: nothing resumes an in-flight background
// ingestion goroutine across a process restart, so a Pending flag left set
// when the store is reopened means that ingestion died without ever
// clearing it. A fresh NewStore must convert it into an explicit error
// instead of leaving the file reading "still indexing…" forever.
func TestNewStoreReconcilesStalePendingFromPreviousProcess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "filedump")

	first, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore (first process): %v", err)
	}
	fStuck, relStuck, err := first.OpenForWrite("", "stuck.pdf")
	if err != nil {
		t.Fatalf("OpenForWrite stuck: %v", err)
	}
	fStuck.Close()
	fOK, relOK, err := first.OpenForWrite("", "fine.txt")
	if err != nil {
		t.Fatalf("OpenForWrite fine: %v", err)
	}
	fOK.Close()
	if err := first.SetPending(relStuck, true); err != nil {
		t.Fatalf("SetPending stuck: %v", err)
	}
	if err := first.LinkDocument(relOK, "doc-fine"); err != nil {
		t.Fatalf("LinkDocument fine: %v", err)
	}

	// Simulate the process restarting: a brand new Store over the same
	// root, as if ingestFileDumpUploadAsync's goroutine for stuck.pdf had
	// simply vanished with the old process.
	second, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore (second process): %v", err)
	}
	listing, err := second.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := make(map[string]FileInfo, len(listing.Files))
	for _, f := range listing.Files {
		byName[f.Name] = f
	}

	stuck := byName["stuck.pdf"]
	if stuck.RAGPending {
		t.Errorf("stuck.pdf still RAGPending after a fresh Store, want it converted to an error")
	}
	if stuck.RAGError == "" {
		t.Errorf("stuck.pdf has no RAGError after reconciliation, want an explicit message")
	}

	// A file that had already finished ingesting before the restart must
	// be left completely alone.
	fine := byName["fine.txt"]
	if !fine.InRAG || fine.DocumentID != "doc-fine" || fine.RAGPending || fine.RAGError != "" {
		t.Errorf("fine.txt was disturbed by reconciliation, got %+v", fine)
	}
}

func TestNewStoreCreatesRootAndSidecarIsSibling(t *testing.T) {
	store := newTestStore(t)
	if info, err := os.Stat(store.Root()); err != nil || !info.IsDir() {
		t.Fatalf("expected root directory to exist, got err=%v", err)
	}
	if filepath.Dir(store.sidecarPath) == store.Root() {
		t.Fatalf("sidecar must not live inside the browsable root, got %s", store.sidecarPath)
	}
}

func TestCreateFolderAndList(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if err := store.CreateFolder("docs", "ford"); err != nil {
		t.Fatalf("CreateFolder nested: %v", err)
	}

	listing, err := store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Folders) != 1 || listing.Folders[0].Name != "docs" {
		t.Fatalf("expected one folder 'docs', got %+v", listing.Folders)
	}

	nested, err := store.List("docs")
	if err != nil {
		t.Fatalf("List nested: %v", err)
	}
	if len(nested.Folders) != 1 || nested.Folders[0].Name != "ford" {
		t.Fatalf("expected nested folder 'ford', got %+v", nested.Folders)
	}
}

func TestCreateFolderRejectsPathSeparatorNames(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "a/b"); err == nil {
		t.Fatal("expected error for folder name containing a separator")
	}
	if err := store.CreateFolder("", "../escape"); err == nil {
		t.Fatal("expected error for folder name containing a separator")
	}
}

func TestSafeJoinConfinesTraversalAttempts(t *testing.T) {
	// safeJoin's leading-slash trick means "../../etc" resolves to
	// root/etc (a plain, harmless subpath), not an error — Clean("/" +
	// userPath) absorbs any leading ".." components before they can
	// walk above root. What matters is that it can never resolve outside
	// root, not that it rejects every "../"-containing string.
	store := newTestStore(t)
	listing, err := store.List("../../etc")
	if err != nil {
		t.Fatalf("expected traversal attempt to be confined, not errored: %v", err)
	}
	if len(listing.Files) != 0 || len(listing.Folders) != 0 {
		t.Fatalf("expected empty listing for a nonexistent confined path, got %+v", listing)
	}

	if err := store.CreateFolder("../escape", "x"); err != nil {
		t.Fatalf("expected the parent-dir escape attempt to be confined, not errored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "escape", "x")); err != nil {
		t.Fatalf("expected folder to land inside root at escape/x, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Root()), "escape")); !os.IsNotExist(err) {
		t.Fatalf("expected nothing created outside root, stat err=%v", err)
	}
}

func TestOpenForWriteAndListShowsFile(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	f, rel, err := store.OpenForWrite("docs", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	if rel != "docs/manual.txt" {
		t.Fatalf("expected rel path docs/manual.txt, got %q", rel)
	}
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	listing, err := store.List("docs")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Files) != 1 || listing.Files[0].Name != "manual.txt" {
		t.Fatalf("expected one file 'manual.txt', got %+v", listing.Files)
	}
	if listing.Files[0].Size != 5 {
		t.Fatalf("expected size 5, got %d", listing.Files[0].Size)
	}
	if listing.Files[0].InRAG {
		t.Fatal("expected InRAG false before LinkDocument is called")
	}
}

func TestOpenForWriteRejectsPathSeparatorFilenames(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := store.OpenForWrite("", "sub/dir/file.txt"); err == nil {
		t.Fatal("expected error for filename containing a separator")
	}
}

func TestLinkDocumentSurfacesInListing(t *testing.T) {
	store := newTestStore(t)
	f, rel, err := store.OpenForWrite("", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()

	if err := store.LinkDocument(rel, "doc-123"); err != nil {
		t.Fatalf("LinkDocument: %v", err)
	}

	listing, err := store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !listing.Files[0].InRAG || listing.Files[0].DocumentID != "doc-123" {
		t.Fatalf("expected file linked to doc-123, got %+v", listing.Files[0])
	}

	id, ok, err := store.DocumentID(rel)
	if err != nil || !ok || id != "doc-123" {
		t.Fatalf("DocumentID: id=%q ok=%v err=%v", id, ok, err)
	}
}

func TestSetPendingSurfacesInListing(t *testing.T) {
	store := newTestStore(t)
	f, rel, err := store.OpenForWrite("", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()

	if err := store.SetPending(rel, true); err != nil {
		t.Fatalf("SetPending: %v", err)
	}
	listing, err := store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !listing.Files[0].RAGPending {
		t.Fatalf("expected RAGPending true, got %+v", listing.Files[0])
	}

	if err := store.SetPending(rel, false); err != nil {
		t.Fatalf("SetPending(false): %v", err)
	}
	listing, err = store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Files[0].RAGPending {
		t.Fatalf("expected RAGPending cleared, got %+v", listing.Files[0])
	}
}

func TestSetIngestErrorSurfacesInListingAndClearsPending(t *testing.T) {
	store := newTestStore(t)
	f, rel, err := store.OpenForWrite("", "manual.pdf")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()
	if err := store.SetPending(rel, true); err != nil {
		t.Fatalf("SetPending: %v", err)
	}

	if err := store.SetIngestError(rel, "could not process PDF for search: boom"); err != nil {
		t.Fatalf("SetIngestError: %v", err)
	}
	listing, err := store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Files[0].RAGPending {
		t.Errorf("expected RAGPending cleared once an error is recorded, got %+v", listing.Files[0])
	}
	if listing.Files[0].RAGError != "could not process PDF for search: boom" {
		t.Errorf("RAGError = %q, want the recorded message", listing.Files[0].RAGError)
	}

	// A later successful ingestion (LinkDocument) must clear the error.
	if err := store.LinkDocument(rel, "doc-123"); err != nil {
		t.Fatalf("LinkDocument: %v", err)
	}
	listing, err = store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Files[0].RAGError != "" {
		t.Errorf("RAGError = %q, want cleared after a successful re-ingestion", listing.Files[0].RAGError)
	}
	if !listing.Files[0].InRAG || listing.Files[0].DocumentID != "doc-123" {
		t.Errorf("expected file linked to doc-123, got %+v", listing.Files[0])
	}
}

func TestMoveFilePreservesSidecarLink(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "dest"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	f, rel, err := store.OpenForWrite("", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()
	if err := store.LinkDocument(rel, "doc-1"); err != nil {
		t.Fatalf("LinkDocument: %v", err)
	}

	updates, err := store.Move("manual.txt", "dest/manual.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if len(updates) != 1 || updates[0].DocumentID != "doc-1" || updates[0].NewPath != "dest/manual.txt" {
		t.Fatalf("unexpected updates: %+v", updates)
	}

	id, ok, err := store.DocumentID("dest/manual.txt")
	if err != nil || !ok || id != "doc-1" {
		t.Fatalf("DocumentID after move: id=%q ok=%v err=%v", id, ok, err)
	}
	if _, ok, _ := store.DocumentID("manual.txt"); ok {
		t.Fatal("expected old path to no longer be linked")
	}
}

func TestMoveFolderCarriesNestedLinks(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if err := store.CreateFolder("docs", "ford"); err != nil {
		t.Fatalf("CreateFolder nested: %v", err)
	}
	f, rel, err := store.OpenForWrite("docs/ford", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()
	if err := store.LinkDocument(rel, "doc-9"); err != nil {
		t.Fatalf("LinkDocument: %v", err)
	}

	updates, err := store.Move("docs/ford", "docs/ford-renamed")
	if err != nil {
		t.Fatalf("Move folder: %v", err)
	}
	if len(updates) != 1 || updates[0].NewPath != "docs/ford-renamed/manual.txt" {
		t.Fatalf("unexpected updates: %+v", updates)
	}
	if _, ok, _ := store.DocumentID("docs/ford-renamed/manual.txt"); !ok {
		t.Fatal("expected link to follow the moved folder")
	}
}

func TestDeleteFileRemovesSidecarLink(t *testing.T) {
	store := newTestStore(t)
	f, rel, err := store.OpenForWrite("", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()
	if err := store.LinkDocument(rel, "doc-1"); err != nil {
		t.Fatalf("LinkDocument: %v", err)
	}

	result, err := store.Delete("manual.txt", false)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(result.Paths) != 1 || len(result.DocumentIDs) != 1 || result.DocumentIDs[0] != "doc-1" {
		t.Fatalf("unexpected delete result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "manual.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, stat err=%v", err)
	}
}

func TestDeleteNonEmptyFolderRequiresRecursive(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	f, _, err := store.OpenForWrite("docs", "manual.txt")
	if err != nil {
		t.Fatalf("OpenForWrite: %v", err)
	}
	f.Close()

	if _, err := store.Delete("docs", false); err == nil {
		t.Fatal("expected non-recursive delete of a non-empty folder to fail")
	}

	result, err := store.Delete("docs", true)
	if err != nil {
		t.Fatalf("recursive Delete: %v", err)
	}
	if len(result.Paths) != 1 || result.Paths[0] != "docs/manual.txt" {
		t.Fatalf("unexpected recursive delete result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "docs")); !os.IsNotExist(err) {
		t.Fatalf("expected folder to be removed, stat err=%v", err)
	}
}

func TestDeleteFolderCascadesDocumentIDs(t *testing.T) {
	store := newTestStore(t)
	if err := store.CreateFolder("", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	f1, rel1, err := store.OpenForWrite("docs", "a.txt")
	if err != nil {
		t.Fatalf("OpenForWrite a: %v", err)
	}
	f1.Close()
	f2, rel2, err := store.OpenForWrite("docs", "b.txt")
	if err != nil {
		t.Fatalf("OpenForWrite b: %v", err)
	}
	f2.Close()
	if err := store.LinkDocument(rel1, "doc-a"); err != nil {
		t.Fatalf("LinkDocument a: %v", err)
	}
	if err := store.LinkDocument(rel2, "doc-b"); err != nil {
		t.Fatalf("LinkDocument b: %v", err)
	}

	result, err := store.Delete("docs", true)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(result.DocumentIDs) != 2 {
		t.Fatalf("expected 2 cascaded document ids, got %+v", result.DocumentIDs)
	}
}
