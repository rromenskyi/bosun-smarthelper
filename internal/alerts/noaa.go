package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// noaaFeatureCollection is weather.gov's active-alerts response shape —
// confirmed live against the real API (see docs/alerts.md), not assumed
// from memory: a GeoJSON FeatureCollection whose properties carry a
// unique id, event type, severity, and human-readable text.
type noaaFeatureCollection struct {
	Features []struct {
		Properties struct {
			ID          string `json:"id"`
			Event       string `json:"event"`
			Severity    string `json:"severity"`
			Headline    string `json:"headline"`
			Description string `json:"description"`
			Instruction string `json:"instruction"`
			AreaDesc    string `json:"areaDesc"`
		} `json:"properties"`
	} `json:"features"`
}

// noaaBaseURL is a var (not a const) purely so tests can point it at an
// httptest server instead of the real weather.gov.
var noaaBaseURL = "https://api.weather.gov"

// FetchNOAAAlerts returns every currently active NWS alert covering the
// given point — https://api.weather.gov/alerts/active?point=lat,lon — no
// API key needed. US territory only; weather.gov has no coverage outside
// it and simply returns an empty feature list rather than erroring, so
// this never fails just because a boat is somewhere it doesn't cover.
func FetchNOAAAlerts(ctx context.Context, latitude, longitude float64) ([]Alert, error) {
	url := fmt.Sprintf("%s/alerts/active?point=%g,%g", noaaBaseURL, latitude, longitude)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create NOAA request: %w", err)
	}
	// weather.gov asks every client to identify itself with a real
	// User-Agent — not sending one risks more aggressive rate limiting,
	// not a hard failure, but there's no reason to risk it.
	req.Header.Set("User-Agent", "bosun-smarthelper (https://github.com/rromenskyi/bosun-smarthelper)")
	req.Header.Set("Accept", "application/geo+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch NOAA alerts: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("NOAA API returned HTTP %d", resp.StatusCode)
	}
	var result noaaFeatureCollection
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode NOAA response: %w", err)
	}

	now := time.Now()
	alerts := make([]Alert, 0, len(result.Features))
	for _, f := range result.Features {
		p := f.Properties
		body := p.Headline
		if body == "" {
			body = p.Description
		}
		if p.Instruction != "" {
			body += "\n\n" + p.Instruction
		}
		alerts = append(alerts, Alert{
			Source:   "noaa",
			Severity: noaaSeverity(p.Severity),
			Title:    fmt.Sprintf("%s: %s", p.Event, p.AreaDesc),
			Body:     body,
			At:       now,
			ID:       p.ID,
		})
	}
	return alerts, nil
}

// noaaSeverity maps weather.gov's own severity scale (Extreme, Severe,
// Moderate, Minor, Unknown — see
// https://www.weather.gov/documentation/services-web-api#/default/get_alerts)
// onto this package's smaller one; anything below Severe still reaches
// every configured channel; the field is for a channel to use in its own
// formatting, e.g. deciding whether to say the alert out loud twice.
func noaaSeverity(raw string) Severity {
	switch raw {
	case "Extreme":
		return SeverityExtreme
	case "Severe":
		return SeveritySevere
	default:
		return SeverityWarning
	}
}

// CheckNOAA fetches currently active alerts for the given point, notifies
// about every one not already in seenIDs, and returns the full set of
// currently-active IDs — the caller persists it (see SaveNOAASeenIDs) so
// a restart doesn't re-notify about an alert already reported before it,
// and so an alert that's no longer active naturally drops out and would
// be treated as new if its exact ID were ever reused (NOAA's IDs are
// unique per issuance, so this is a theoretical safeguard, not an
// observed necessity).
func CheckNOAA(ctx context.Context, latitude, longitude float64, seenIDs map[string]bool, notifiers []Notifier) (map[string]bool, []error) {
	alerts, err := FetchNOAAAlerts(ctx, latitude, longitude)
	if err != nil {
		return seenIDs, []error{err}
	}
	next := make(map[string]bool, len(alerts))
	var errs []error
	for _, alert := range alerts {
		next[alert.ID] = true
		if seenIDs[alert.ID] {
			continue
		}
		for _, notifier := range notifiers {
			if err := notifier.Notify(ctx, alert); err != nil {
				errs = append(errs, fmt.Errorf("notify %T: %w", notifier, err))
			}
		}
	}
	return next, errs
}
