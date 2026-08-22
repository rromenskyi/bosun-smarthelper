package alerts

import (
	"context"
	"fmt"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/metrics"
)

// Threshold is one configured limit to watch. Metric must be a name
// internal/metrics.Store already has samples for — any of config.yaml's
// metrics.sources, built-in (disk_used_percent, cpu_temp_c, ...) or a
// future custom one (battery charge, a tank level, anything a sensor tool
// reports) — this package never hardcodes what a metric physically means.
type Threshold struct {
	Metric   string
	Operator string // ">", "<", ">=", "<=", "=="
	Value    float64
	Title    string // human label for the alert text, e.g. "Grey water tank"; defaults to Metric if empty
}

func (t Threshold) crossed(current float64) (bool, error) {
	switch t.Operator {
	case ">":
		return current > t.Value, nil
	case "<":
		return current < t.Value, nil
	case ">=":
		return current >= t.Value, nil
	case "<=":
		return current <= t.Value, nil
	case "==":
		return current == t.Value, nil
	default:
		return false, fmt.Errorf("unknown threshold operator %q for metric %q", t.Operator, t.Metric)
	}
}

// ThresholdChecker watches a fixed list of thresholds against
// internal/metrics' latest recorded samples, notifying only on a state
// *transition* (normal -> crossed, and crossed -> normal again) — never
// repeatedly on every tick while a threshold stays crossed, which would
// just be noise for something already reported once.
type ThresholdChecker struct {
	Store      *metrics.Store
	Thresholds []Threshold
	Notifiers  []Notifier
}

// Check evaluates every configured threshold once, sending an alert
// through every notifier for each one that just changed state since the
// last call. state is the crossed/not-crossed map Check itself returned
// last time — the caller owns persisting it across restarts (see
// internal/alerts/state.go); passing an empty/nil map treats every
// currently-crossed threshold as newly crossed, which is the right
// behavior for "we don't know what the state was before, so anything
// crossed right now is worth reporting."
func (c *ThresholdChecker) Check(ctx context.Context, state map[string]bool) (map[string]bool, []error) {
	next := make(map[string]bool, len(c.Thresholds))
	var errs []error
	for _, threshold := range c.Thresholds {
		point, ok, err := c.Store.Latest(ctx, threshold.Metric)
		if err != nil {
			errs = append(errs, fmt.Errorf("read latest %s: %w", threshold.Metric, err))
			continue
		}
		if !ok {
			continue // no sample yet — nothing to compare, and nothing worth reporting either way
		}
		crossed, err := threshold.crossed(point.Value)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next[threshold.Metric] = crossed
		if crossed == state[threshold.Metric] {
			continue
		}
		alert := thresholdAlert(threshold, point.Value, crossed)
		for _, notifier := range c.Notifiers {
			if err := notifier.Notify(ctx, alert); err != nil {
				errs = append(errs, fmt.Errorf("notify %T: %w", notifier, err))
			}
		}
	}
	return next, errs
}

func thresholdAlert(t Threshold, value float64, crossed bool) Alert {
	title := t.Title
	if title == "" {
		title = t.Metric
	}
	if crossed {
		return Alert{
			Source:   "threshold",
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("%s: threshold crossed", title),
			Body:     fmt.Sprintf("%s is %v (threshold: %s %v)", title, value, t.Operator, t.Value),
			At:       time.Now(),
		}
	}
	return Alert{
		Source:   "threshold",
		Severity: SeverityInfo,
		Title:    fmt.Sprintf("%s: back to normal", title),
		Body:     fmt.Sprintf("%s is now %v", title, value),
		At:       time.Now(),
	}
}
