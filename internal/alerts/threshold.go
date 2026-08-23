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
//
// Web-managed, not config.yaml-managed: this is built fresh on every
// check from internal/settings.Data.AlertsThresholds (see
// cmd/smarthelper/main.go's runThresholdChecker) so a rule added, edited,
// or removed from the settings page takes effect on the next tick — see
// docs/alerts.md.
type Threshold struct {
	// ID identifies this specific rule for state-tracking purposes (see
	// Check) — internal/settings.AlertsThresholdRule.ID. Falls back to
	// Metric when empty, so hand-built Threshold values (tests, or any
	// future caller that genuinely has just one rule per metric) don't
	// need to bother setting it.
	ID       string
	Metric   string
	Operator string // ">", "<", ">=", "<=", "=="
	Value    float64
	Title    string // human label for the alert text, e.g. "Grey water tank"; defaults to Metric if empty
	// SmoothingSamples, when > 1, compares a moving average of the last N
	// raw samples (internal/metrics.Store.RecentValues) instead of the
	// single most recent reading — reduces false alarms from a noisy
	// sensor. <= 1 (the default) uses Store.Latest, unchanged from before
	// this field existed.
	SmoothingSamples int
	// CustomText, if set, replaces the auto-generated Body on a *crossed*
	// alert (never on the "back to normal" one) — sent as-is to every
	// enabled Notifier for this rule, since neither Telegram's plain text
	// nor the webhook's JSON shape (source/severity/title/body/at) depend
	// on what string actually goes into the message.
	CustomText string
	// PlaySiren is passed straight through onto the resulting Alert; only
	// SpeakerNotifier acts on it, and only makes sense alongside Speaker
	// actually being one of this rule's Notifiers.
	PlaySiren bool
	// Notifiers is this rule's own channel selection — no longer shared
	// across every threshold the way it once was, since each rule now
	// picks its own subset of Telegram/Webhook/Speaker.
	Notifiers []Notifier
}

func (t Threshold) stateKey() string {
	if t.ID != "" {
		return t.ID
	}
	return t.Metric
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

// currentValue returns the value to compare against this threshold's
// bound: the single latest raw sample, or — with SmoothingSamples > 1 — a
// moving average over the last N raw samples.
func (t Threshold) currentValue(ctx context.Context, store *metrics.Store) (value float64, ok bool, err error) {
	if t.SmoothingSamples <= 1 {
		point, ok, err := store.Latest(ctx, t.Metric)
		if err != nil {
			return 0, false, fmt.Errorf("read latest %s: %w", t.Metric, err)
		}
		return point.Value, ok, nil
	}
	values, err := store.RecentValues(ctx, t.Metric, t.SmoothingSamples)
	if err != nil {
		return 0, false, fmt.Errorf("read recent %s: %w", t.Metric, err)
	}
	if len(values) == 0 {
		return 0, false, nil
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values)), true, nil
}

// ThresholdChecker watches a list of rules against internal/metrics'
// samples, notifying only on a state *transition* (normal -> crossed, and
// crossed -> normal again) — never repeatedly on every tick while a rule
// stays crossed, which would just be noise for something already
// reported once.
type ThresholdChecker struct {
	Store      *metrics.Store
	Thresholds []Threshold
}

// Check evaluates every configured rule once, sending an alert through
// that rule's own Notifiers for each one that just changed state since
// the last call. state is the crossed/not-crossed map Check itself
// returned last time, keyed by Threshold.stateKey() — the caller owns
// persisting it across restarts (see internal/alerts/state.go); passing
// an empty/nil map treats every currently-crossed rule as newly crossed,
// the right behavior for "we don't know what the state was before, so
// anything crossed right now is worth reporting."
func (c *ThresholdChecker) Check(ctx context.Context, state map[string]bool) (map[string]bool, []error) {
	next := make(map[string]bool, len(c.Thresholds))
	var errs []error
	for _, threshold := range c.Thresholds {
		key := threshold.stateKey()
		value, ok, err := threshold.currentValue(ctx, c.Store)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue // no sample yet — nothing to compare, and nothing worth reporting either way
		}
		crossed, err := threshold.crossed(value)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next[key] = crossed
		if crossed == state[key] {
			continue
		}
		alert := thresholdAlert(threshold, value, crossed)
		for _, notifier := range threshold.Notifiers {
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
		body := fmt.Sprintf("%s is %v (threshold: %s %v)", title, value, t.Operator, t.Value)
		if t.CustomText != "" {
			body = t.CustomText
		}
		return Alert{
			Source:    "threshold",
			Severity:  SeverityWarning,
			Title:     fmt.Sprintf("%s: threshold crossed", title),
			Body:      body,
			At:        time.Now(),
			PlaySiren: t.PlaySiren,
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
