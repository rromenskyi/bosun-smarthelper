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

func TestUpdateNormalizesThresholdRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := Load(path, Data{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := store.Update(Data{AlertsThresholds: []AlertsThresholdRule{
		{Metric: " disk_used_percent ", Operator: ">", Value: 90, Title: "  Disk  ", SmoothingSamples: 0},
		{ID: "kept-id", Metric: "cpu_percent", Operator: ">", Value: 95, SmoothingSamples: 5},
	}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := store.Get().AlertsThresholds
	if len(got) != 2 {
		t.Fatalf("rules = %+v, want 2", got)
	}
	if got[0].ID == "" {
		t.Error("rule with no ID should have been assigned one")
	}
	if got[0].Metric != "disk_used_percent" || got[0].Title != "Disk" {
		t.Errorf("rule[0] = %+v, want trimmed metric/title", got[0])
	}
	if got[0].SmoothingSamples != 1 {
		t.Errorf("SmoothingSamples = %d, want clamped to 1 (0 means \"no smoothing\", not \"no data\")", got[0].SmoothingSamples)
	}
	if got[1].ID != "kept-id" {
		t.Errorf("rule[1].ID = %q, want the caller-supplied ID preserved", got[1].ID)
	}
	if got[1].SmoothingSamples != 5 {
		t.Errorf("SmoothingSamples = %d, want the caller-supplied 5 preserved", got[1].SmoothingSamples)
	}
}
