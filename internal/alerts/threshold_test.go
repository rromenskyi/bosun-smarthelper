package alerts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/metrics"
)

type recordingNotifier struct {
	alerts []Alert
}

func (r *recordingNotifier) Notify(_ context.Context, alert Alert) error {
	r.alerts = append(r.alerts, alert)
	return nil
}

func openTestMetricsStore(t *testing.T) *metrics.Store {
	t.Helper()
	store, err := metrics.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("metrics.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestThresholdCheckerFiresOnceOnCrossing(t *testing.T) {
	store := openTestMetricsStore(t)
	ctx := context.Background()
	if err := store.Insert(ctx, time.Now(), "disk_used_percent", 95); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	notifier := &recordingNotifier{}
	checker := &ThresholdChecker{
		Store:      store,
		Thresholds: []Threshold{{Metric: "disk_used_percent", Operator: ">", Value: 90, Title: "Disk"}},
		Notifiers:  []Notifier{notifier},
	}

	state, errs := checker.Check(ctx, map[string]bool{})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if !state["disk_used_percent"] {
		t.Error("state[disk_used_percent] = false, want true — it's over the threshold")
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %+v, want exactly 1", notifier.alerts)
	}
	if notifier.alerts[0].Severity != SeverityWarning {
		t.Errorf("severity = %v, want warning", notifier.alerts[0].Severity)
	}

	// Second check, still crossed, same state passed in — must not fire again.
	state, errs = checker.Check(ctx, state)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(notifier.alerts) != 1 {
		t.Errorf("alerts = %+v after a second still-crossed check, want still 1 (no repeat spam)", notifier.alerts)
	}
}

func TestThresholdCheckerFiresRecoveryAlert(t *testing.T) {
	store := openTestMetricsStore(t)
	ctx := context.Background()
	if err := store.Insert(ctx, time.Now(), "disk_used_percent", 50); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	notifier := &recordingNotifier{}
	checker := &ThresholdChecker{
		Store:      store,
		Thresholds: []Threshold{{Metric: "disk_used_percent", Operator: ">", Value: 90}},
		Notifiers:  []Notifier{notifier},
	}

	// Starts already marked crossed (as if we'd alerted last time), but
	// the latest sample is back under the threshold now.
	_, errs := checker.Check(ctx, map[string]bool{"disk_used_percent": true})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %+v, want exactly 1 recovery alert", notifier.alerts)
	}
	if notifier.alerts[0].Severity != SeverityInfo {
		t.Errorf("severity = %v, want info for a recovery", notifier.alerts[0].Severity)
	}
}

func TestThresholdCheckerSkipsMetricWithNoSamples(t *testing.T) {
	store := openTestMetricsStore(t)
	notifier := &recordingNotifier{}
	checker := &ThresholdChecker{
		Store:      store,
		Thresholds: []Threshold{{Metric: "battery_percent", Operator: "<", Value: 20}},
		Notifiers:  []Notifier{notifier},
	}
	state, errs := checker.Check(context.Background(), map[string]bool{})
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none for a metric with simply no samples yet", errs)
	}
	if len(notifier.alerts) != 0 {
		t.Errorf("alerts = %+v, want none", notifier.alerts)
	}
	if _, ok := state["battery_percent"]; ok {
		t.Error("state has an entry for a metric with no samples, want none")
	}
}

func TestThresholdCrossedOperators(t *testing.T) {
	cases := []struct {
		op      string
		value   float64
		current float64
		want    bool
	}{
		{">", 90, 95, true}, {">", 90, 85, false},
		{"<", 20, 15, true}, {"<", 20, 25, false},
		{">=", 90, 90, true}, {"<=", 20, 20, true},
		{"==", 0, 0, true}, {"==", 0, 1, false},
	}
	for _, c := range cases {
		got, err := Threshold{Operator: c.op, Value: c.value}.crossed(c.current)
		if err != nil {
			t.Errorf("crossed(%v %s %v): %v", c.current, c.op, c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("crossed(%v %s %v) = %v, want %v", c.current, c.op, c.value, got, c.want)
		}
	}
}

func TestThresholdCrossedRejectsUnknownOperator(t *testing.T) {
	if _, err := (Threshold{Operator: "~="}).crossed(1); err == nil {
		t.Error("expected an error for an unknown operator")
	}
}
