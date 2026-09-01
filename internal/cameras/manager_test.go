package cameras

import (
	"context"
	"testing"
	"time"
)

func TestNewManagerBuildsOneRelayPerCamera(t *testing.T) {
	manager := NewManager([]Config{
		{Name: "front", LabelRU: "Перед", LabelEN: "Front", StreamURL: "http://front.example/stream"},
		{Name: "stern", LabelRU: "Корма", LabelEN: "Stern", StreamURL: "http://stern.example/stream"},
	}, discardLogger())

	front, ok := manager.Relay("front")
	if !ok {
		t.Fatal("expected a relay named front")
	}
	if front.StreamURL != "http://front.example/stream" {
		t.Errorf("front relay StreamURL = %q, want the configured URL", front.StreamURL)
	}

	stern, ok := manager.Relay("stern")
	if !ok {
		t.Fatal("expected a relay named stern")
	}
	if stern.StreamURL != "http://stern.example/stream" {
		t.Errorf("stern relay StreamURL = %q, want the configured URL", stern.StreamURL)
	}
}

func TestManagerRelayReportsUnknownCameraNotFound(t *testing.T) {
	manager := NewManager([]Config{{Name: "front", StreamURL: "http://front.example/stream"}}, discardLogger())
	if _, ok := manager.Relay("nonexistent"); ok {
		t.Error("expected ok=false for an unconfigured camera name")
	}
}

// TestManagerListReturnsACopy guards List's own doc comment ("not aliasing
// internal state"): a caller mutating the returned slice must never affect
// what a later List() call returns.
func TestManagerListReturnsACopy(t *testing.T) {
	original := []Config{
		{Name: "front", LabelRU: "Перед", LabelEN: "Front", StreamURL: "http://front.example/stream"},
		{Name: "stern", LabelRU: "Корма", LabelEN: "Stern", StreamURL: "http://stern.example/stream"},
	}
	manager := NewManager(original, discardLogger())

	list := manager.List()
	if len(list) != 2 {
		t.Fatalf("List() = %d entries, want 2", len(list))
	}
	list[0].Name = "tampered"

	again := manager.List()
	for _, c := range again {
		if c.Name == "tampered" {
			t.Fatal("mutating a previous List() result affected a later call — List must return an independent copy")
		}
	}
	if again[0].Name != "front" && again[1].Name != "front" {
		t.Errorf("List() = %#v, want the original front entry untouched", again)
	}
}

func TestManagerListEmptyWhenNoCamerasConfigured(t *testing.T) {
	manager := NewManager(nil, discardLogger())
	if list := manager.List(); len(list) != 0 {
		t.Errorf("List() = %#v, want empty for no configured cameras", list)
	}
}

// TestManagerStartRunsEveryConfiguredRelay proves Start actually launches a
// working goroutine per camera — not just that it returns without
// panicking — by pointing two relays at two distinct fake camera servers
// and waiting for both to report a real connection.
func TestManagerStartRunsEveryConfiguredRelay(t *testing.T) {
	// Several frames, each paced 5ms apart by fakeCameraServer itself,
	// so the connection stays open long enough for the poll loop below
	// to reliably observe Connected()==true before the server finishes
	// and the handler returns on its own — an unclosed ready gate would
	// hold the handler open forever and deadlock t.Cleanup's
	// server.Close(), which waits for the handler to return.
	frames := make([][]byte, 20)
	for i := range frames {
		frames[i] = []byte("frame")
	}
	serverA := fakeCameraServer(t, frames, nil)
	serverB := fakeCameraServer(t, frames, nil)

	manager := NewManager([]Config{
		{Name: "a", StreamURL: serverA.URL},
		{Name: "b", StreamURL: serverB.URL},
	}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	relayA, _ := manager.Relay("a")
	relayB, _ := manager.Relay("b")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if relayA.Connected() && relayB.Connected() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for both relays to connect: a=%v b=%v", relayA.Connected(), relayB.Connected())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
