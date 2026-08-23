// Package alerts delivers a small number of genuinely urgent notifications
// — a NOAA weather alert for the current position, or a configured metric
// (disk space, battery charge, a tank level, anything internal/metrics
// already samples) crossing a threshold — through whichever channels are
// both configured (config.yaml/.env) and enabled (the settings page):
// Telegram, a webhook, or speaking through the host's own speaker.
//
// Deliberately narrow: this is not a general pub/sub or logging system,
// just "something is wrong enough that a human should hear about it right
// now, wherever they are" for the two sources above.
package alerts

import (
	"context"
	"time"
)

// Severity mirrors NOAA's own alert severity scale (see
// https://www.weather.gov/documentation/services-web-api#/default/get_alerts)
// loosely enough to also fit a threshold crossing — "warning" for most
// threshold alerts, "severe"/"extreme" reserved for NOAA's own most urgent
// categories.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeveritySevere  Severity = "severe"
	SeverityExtreme Severity = "extreme"
)

// Alert is one notification-worthy event, independent of which channel(s)
// eventually deliver it.
type Alert struct {
	Source   string // "noaa" or "threshold"
	Severity Severity
	Title    string
	Body     string
	At       time.Time
	// ID identifies this specific alert for dedup purposes — NOAA's own
	// unique urn for its alerts, empty for threshold alerts (which dedup
	// by metric name and crossed/not-crossed state instead, see
	// ThresholdChecker).
	ID string
	// PlaySiren asks SpeakerNotifier to play a short built-in siren sound
	// before speaking Title/Body — ignored by every other Notifier. Set
	// per threshold rule (internal/settings.AlertsThresholdRule.Siren),
	// never for NOAA alerts.
	PlaySiren bool
}

// Notifier delivers one alert through one channel. Each implementation
// owns its own formatting — a Telegram message, a webhook's JSON body,
// spoken text — since "the same alert, rendered for very different
// media" isn't a single shared format worth forcing.
type Notifier interface {
	Notify(ctx context.Context, alert Alert) error
}
