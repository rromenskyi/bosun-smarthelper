package alerts

import (
	"testing"
)

func TestThresholdStateRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := map[string]bool{"disk_used_percent": true, "cpu_temp_c": false}
	if err := SaveThresholdState(dir, want); err != nil {
		t.Fatalf("SaveThresholdState: %v", err)
	}
	got, err := LoadThresholdState(dir)
	if err != nil {
		t.Fatalf("LoadThresholdState: %v", err)
	}
	cpuTempC, cpuTempCPresent := got["cpu_temp_c"]
	if len(got) != 2 || got["disk_used_percent"] != true || !cpuTempCPresent || cpuTempC != false {
		t.Errorf("got = %+v, want %+v (cpu_temp_c must round-trip as an explicit false, not just be absent)", got, want)
	}
}

func TestThresholdStateEmptyWithoutPriorSave(t *testing.T) {
	state, err := LoadThresholdState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadThresholdState: %v", err)
	}
	if len(state) != 0 {
		t.Errorf("state = %+v, want empty", state)
	}
}

func TestNOAASeenIDsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := map[string]bool{"urn:oid:1.2.3": true, "urn:oid:4.5.6": true}
	if err := SaveNOAASeenIDs(dir, want); err != nil {
		t.Fatalf("SaveNOAASeenIDs: %v", err)
	}
	got, err := LoadNOAASeenIDs(dir)
	if err != nil {
		t.Fatalf("LoadNOAASeenIDs: %v", err)
	}
	if len(got) != 2 || !got["urn:oid:1.2.3"] || !got["urn:oid:4.5.6"] {
		t.Errorf("got = %+v, want %+v", got, want)
	}
}

func TestNOAASeenIDsEmptyWithoutPriorSave(t *testing.T) {
	ids, err := LoadNOAASeenIDs(t.TempDir())
	if err != nil {
		t.Fatalf("LoadNOAASeenIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %+v, want empty", ids)
	}
}
