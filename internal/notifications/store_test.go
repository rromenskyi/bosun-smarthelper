package notifications

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "notifications.json"))
}

func TestAddAssignsIDAndTimestamp(t *testing.T) {
	store := newTestStore(t)
	n, err := store.Add(Notification{Source: "threshold", Title: "Battery low", Body: "12.1V"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if n.ID == "" {
		t.Error("expected a generated ID")
	}
	if n.At.IsZero() {
		t.Error("expected At to default to now")
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	store := newTestStore(t)
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now()
	if _, err := store.Add(Notification{Title: "Older", At: older}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Add(Notification{Title: "Newer", At: newer}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Title != "Newer" || list[1].Title != "Older" {
		t.Fatalf("list = %#v, want [Newer, Older]", list)
	}
}

func TestUnreadCount(t *testing.T) {
	store := newTestStore(t)
	first, err := store.Add(Notification{Title: "A"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Add(Notification{Title: "B"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	count, err := store.UnreadCount()
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("UnreadCount = %d, want 2", count)
	}

	if err := store.MarkRead(first.ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	count, err = store.UnreadCount()
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("UnreadCount after MarkRead = %d, want 1", count)
	}
}

func TestMarkAllRead(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Add(Notification{Title: "A"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Add(Notification{Title: "B"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.MarkAllRead(); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	count, err := store.UnreadCount()
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("UnreadCount after MarkAllRead = %d, want 0", count)
	}
}

func TestMarkReadUnknownIDIsNotAnError(t *testing.T) {
	store := newTestStore(t)
	if err := store.MarkRead("never-existed"); err != nil {
		t.Errorf("MarkRead(unknown) = %v, want nil", err)
	}
}

func TestDeleteRemovesNotification(t *testing.T) {
	store := newTestStore(t)
	n, err := store.Add(Notification{Title: "A"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Add(Notification{Title: "B"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Delete(n.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Title != "B" {
		t.Fatalf("list = %#v, want only B", list)
	}
}

func TestDeleteUnknownIDIsNotAnError(t *testing.T) {
	store := newTestStore(t)
	if err := store.Delete("never-existed"); err != nil {
		t.Errorf("Delete(unknown) = %v, want nil", err)
	}
}

func TestAddDedupedSuppressesRepeatWithinWindow(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddDeduped(Notification{Source: "backup", Title: "Backup failed"}, time.Hour); err != nil {
		t.Fatalf("AddDeduped (first): %v", err)
	}
	got, err := store.AddDeduped(Notification{Source: "backup", Title: "Backup failed"}, time.Hour)
	if err != nil {
		t.Fatalf("AddDeduped (repeat): %v", err)
	}
	if got.ID != "" {
		t.Errorf("expected the repeat to be suppressed (zero Notification), got %#v", got)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %#v, want exactly 1 (the repeat must not have been added)", list)
	}
}

func TestAddDedupedAllowsRepeatAfterWindow(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddDeduped(Notification{Source: "backup", Title: "Backup failed", At: time.Now().Add(-2 * time.Hour)}, time.Hour); err != nil {
		t.Fatalf("AddDeduped (first): %v", err)
	}
	got, err := store.AddDeduped(Notification{Source: "backup", Title: "Backup failed"}, time.Hour)
	if err != nil {
		t.Fatalf("AddDeduped (after window): %v", err)
	}
	if got.ID == "" {
		t.Error("expected a notification past the dedup window to be added")
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %#v, want 2 (window elapsed, both should exist)", list)
	}
}

func TestAddDedupedAllowsDifferentTitle(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddDeduped(Notification{Source: "backup", Title: "Backup failed"}, time.Hour); err != nil {
		t.Fatalf("AddDeduped: %v", err)
	}
	got, err := store.AddDeduped(Notification{Source: "backup", Title: "Backup succeeded"}, time.Hour)
	if err != nil {
		t.Fatalf("AddDeduped (different title): %v", err)
	}
	if got.ID == "" {
		t.Error("expected a different title to not be suppressed")
	}
}

func TestAddTrimsOldestPastCap(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < maxNotifications+10; i++ {
		if _, err := store.Add(Notification{Title: "n", At: time.Now().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != maxNotifications {
		t.Fatalf("list length = %d, want %d", len(list), maxNotifications)
	}
}

func TestWritesSurviveFreshInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	first := NewStore(path)
	if _, err := first.Add(Notification{Title: "Persisted"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	second := NewStore(path)
	list, err := second.List()
	if err != nil {
		t.Fatalf("List from fresh instance: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Persisted" {
		t.Fatalf("list from fresh instance = %#v", list)
	}
}
