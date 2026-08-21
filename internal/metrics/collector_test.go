package metrics

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

func TestLookupFieldResolvesNestedPath(t *testing.T) {
	result := map[string]any{
		"cpu_temp_c": 70.0,
		"memory":     map[string]any{"used_percent": 30.0},
	}
	if v, ok := lookupField(result, "cpu_temp_c"); !ok || v != 70.0 {
		t.Errorf("lookupField(cpu_temp_c) = %v, %v, want 70.0, true", v, ok)
	}
	if v, ok := lookupField(result, "memory.used_percent"); !ok || v != 30.0 {
		t.Errorf("lookupField(memory.used_percent) = %v, %v, want 30.0, true", v, ok)
	}
	if _, ok := lookupField(result, "does.not.exist"); ok {
		t.Error("lookupField(does.not.exist) ok = true, want false")
	}
	if _, ok := lookupField(result, "cpu_temp_c.nested"); ok {
		t.Error("lookupField into a scalar's nonexistent child ok = true, want false")
	}
}

func TestNumericValueAveragesWhenConfigured(t *testing.T) {
	if v, ok := numericValue([]float64{10, 20}, "avg"); !ok || v != 15 {
		t.Errorf("numericValue avg = %v, %v, want 15, true", v, ok)
	}
	if _, ok := numericValue([]float64{}, "avg"); ok {
		t.Error("numericValue avg of an empty slice ok = true, want false")
	}
	if _, ok := numericValue(70.0, "avg"); ok {
		t.Error("numericValue avg of a plain float ok = true, want false (wrong shape)")
	}
	if v, ok := numericValue(70.0, ""); !ok || v != 70 {
		t.Errorf("numericValue plain = %v, %v, want 70, true", v, ok)
	}
}

func systemTempSource() config.MetricSource {
	return config.MetricSource{
		Metric: "cpu_temp_c",
		Tool:   "get_system_info",
		Args:   map[string]any{"include": []any{"cpu"}},
		Field:  "cpu_temp_c",
	}
}

func TestSampleAllSkipsUnregisteredTools(t *testing.T) {
	registry := tools.NewRegistry()
	sources := []config.MetricSource{
		{Metric: "gps_speed_kmh", Tool: "get_gps", Field: "speed_kmh"},
	}
	// No GPS tool registered — plenty of deployments won't have that
	// hardware — sampleAll must skip it silently, not fail.
	if samples := sampleAll(context.Background(), registry, sources); len(samples) != 0 {
		t.Errorf("sampleAll() = %+v, want no samples for an unregistered tool", samples)
	}
}

func TestSampleAllExtractsConfiguredFields(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewSystemTool(&config.SystemConfig{}))
	registry.Register(tools.NewFridgeTool(&config.FridgeConfig{Type: "mock", MockFridgeC: 4, MockFreezerC: -18}))

	sources := []config.MetricSource{
		systemTempSource(),
		{Metric: "cpu_percent", Tool: "get_system_info", Args: map[string]any{"include": []any{"cpu"}}, Field: "cpu_percent", Aggregate: "avg"},
		{Metric: "fridge_c", Tool: "get_fridge_temp", Field: "fridge_c"},
		{Metric: "freezer_c", Tool: "get_fridge_temp", Field: "freezer_c"},
	}

	samples := sampleAll(context.Background(), registry, sources)
	got := make(map[string]float64, len(samples))
	for _, s := range samples {
		got[s.Metric] = s.Value
	}

	if got["fridge_c"] != 4 {
		t.Errorf("fridge_c = %v, want 4", got["fridge_c"])
	}
	if got["freezer_c"] != -18 {
		t.Errorf("freezer_c = %v, want -18", got["freezer_c"])
	}
	if _, ok := got["cpu_temp_c"]; !ok {
		t.Error("cpu_temp_c missing — get_system_info should always report something on real hardware in CI too, or this host has no coretemp driver")
	}
}

func TestCollectorCollectOnceWritesAvailableMetrics(t *testing.T) {
	store := openTestStore(t)
	registry := tools.NewRegistry()
	registry.Register(tools.NewFridgeTool(&config.FridgeConfig{Type: "mock", MockFridgeC: 4, MockFreezerC: -18}))

	collector := NewCollector(store, registry, []config.MetricSource{
		{Metric: "fridge_c", Tool: "get_fridge_temp", Field: "fridge_c"},
	}, slog.Default())
	collector.collectOnce(context.Background())

	names, err := store.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(names) != 1 || names[0] != "fridge_c" {
		t.Fatalf("Metrics() = %v, want just fridge_c", names)
	}
}

func TestCollectorRunStopsOnContextCancel(t *testing.T) {
	store := openTestStore(t)
	registry := tools.NewRegistry()
	collector := NewCollector(store, registry, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		collector.Run(ctx, time.Millisecond, time.Hour)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
