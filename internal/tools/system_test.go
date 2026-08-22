package tools

import (
	"context"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/shirou/gopsutil/v4/sensors"
)

func TestRoundPercent(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{5.21, 5},
		{59.28, 59},
		{44.43, 44},
		{99.5, 100},
		{0.4, 0},
	}
	for _, c := range cases {
		if got := roundPercent(c.in); got != c.want {
			t.Errorf("roundPercent(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBytesToGB(t *testing.T) {
	cases := []struct {
		in   uint64
		want float64
	}{
		{7_690_000_000, 8},
		{96_750_000_000, 97},
		{1_000_000_000, 1},
		{1_400_000_000, 1},
		{0, 0},
	}
	for _, c := range cases {
		if got := bytesToGB(c.in); got != c.want {
			t.Errorf("bytesToGB(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAverageTemperatureAveragesOnlyCoreSensors(t *testing.T) {
	// Real sensor keys from this host's coretemp driver (Sandy Bridge i5):
	// the package reading sits close to the max of the cores, not an
	// independent measurement, so it must not skew the per-core average.
	temps := []sensors.TemperatureStat{
		{SensorKey: "coretemp_package_id_0", Temperature: 68},
		{SensorKey: "coretemp_core_0", Temperature: 64},
		{SensorKey: "coretemp_core_1", Temperature: 68},
	}
	got, ok := averageTemperature(temps)
	if !ok {
		t.Fatal("averageTemperature() ok = false, want true")
	}
	if got != 66 {
		t.Errorf("averageTemperature() = %v, want 66 (average of the two core readings, not the package one)", got)
	}
}

func TestAverageTemperatureFallsBackWhenNoCoreSensors(t *testing.T) {
	temps := []sensors.TemperatureStat{
		{SensorKey: "k10temp_tctl", Temperature: 50},
		{SensorKey: "k10temp_tdie", Temperature: 48},
	}
	got, ok := averageTemperature(temps)
	if !ok {
		t.Fatal("averageTemperature() ok = false, want true")
	}
	if got != 49 {
		t.Errorf("averageTemperature() = %v, want 49 (average of all sensors, no \"core\"-labeled ones present)", got)
	}
}

func TestAverageTemperatureNoSensorsReportsNotOK(t *testing.T) {
	if _, ok := averageTemperature(nil); ok {
		t.Error("averageTemperature(nil) ok = true, want false")
	}
}

// TestSystemToolExecuteShape checks the result's structure, not exact
// values (real hardware, no mock backend) — cpu is its own nested object
// like memory/disk, cpu_percent is a plain number rather than a
// single-element slice, and none of the Go-process internals
// (go_version, goroutines, boot_time) that used to ride along are still
// there — see the comments in Execute for why.
func TestSystemToolExecuteShape(t *testing.T) {
	tool := NewSystemTool(&config.SystemConfig{})
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data := result.(map[string]any)

	for _, dropped := range []string{"go_version", "goroutines", "boot_time", "cpu_percent", "cpu_cores", "cpu_temp_c"} {
		if _, ok := data[dropped]; ok {
			t.Errorf("result still has top-level %q, want it gone", dropped)
		}
	}

	cpuInfo, ok := data["cpu"].(map[string]any)
	if !ok {
		t.Fatalf("result[\"cpu\"] = %#v, want a nested object like memory/disk", data["cpu"])
	}
	if v, ok := cpuInfo["used_percent"]; ok {
		if _, isFloat := v.(float64); !isFloat {
			t.Errorf("cpu.used_percent = %#v (%T), want a plain float64, not a slice", v, v)
		}
	}
	if _, ok := cpuInfo["cores"]; !ok {
		t.Error("cpu.cores is missing")
	}

	if _, ok := data["uptime"]; !ok {
		t.Error("uptime is missing")
	}
}
