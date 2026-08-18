package settings

import (
	"path/filepath"
	"testing"
)

func TestLoadSeedsFromDefaultsWhenFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	defaults := Data{NameRU: "Старпом", StylePrompt: "  Ahoy  ", RemoteTemperature: 0.8}

	store, err := Load(path, defaults)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := store.Get()
	if got.NameRU != "Старпом" || got.StylePrompt != "Ahoy" || got.RemoteTemperature != 0.8 {
		t.Errorf("seeded data = %#v", got)
	}

	// A second Load must read back the persisted file, not re-seed from
	// different defaults.
	store2, err := Load(path, Data{NameRU: "Different"})
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if got := store2.Get(); got.NameRU != "Старпом" {
		t.Errorf("second load = %#v, want the persisted seed, not the new defaults", got)
	}
}

func TestUpdatePersistsAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := Load(path, Data{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := store.Update(Data{
		NameRU:            "  Капитан  ",
		StylePrompt:       "  Be terse.  ",
		DefaultLanguage:   " en ",
		RemoteTemperature: 0.5,
		CanonicalTags:     []string{" Purchases ", "", "FUEL_system"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := store.Get()
	if got.NameRU != "Капитан" || got.StylePrompt != "Be terse." || got.DefaultLanguage != "en" {
		t.Errorf("normalized fields = %#v", got)
	}
	if len(got.CanonicalTags) != 2 || got.CanonicalTags[0] != "purchases" || got.CanonicalTags[1] != "fuel_system" {
		t.Errorf("canonical tags = %#v, want empty entries dropped and the rest trimmed/lowercased", got.CanonicalTags)
	}

	// Reload from disk to confirm persistence, not just in-memory state.
	reloaded, err := Load(path, Data{})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get().NameRU != "Капитан" {
		t.Errorf("reloaded = %#v, want the update to have persisted", reloaded.Get())
	}
}
