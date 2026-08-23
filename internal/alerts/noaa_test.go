package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This fixture is a trimmed real response, captured live from
// api.weather.gov/alerts/active (see docs/alerts.md) — not hand-guessed.
const noaaFixture = `{
  "type": "FeatureCollection",
  "features": [
    {
      "id": "https://api.weather.gov/alerts/urn:oid:2.49.0.1.840.0.abc123.002.1",
      "type": "Feature",
      "properties": {
        "id": "urn:oid:2.49.0.1.840.0.abc123.002.1",
        "areaDesc": "Crittenden, AR; Shelby, TN; Tipton, TN",
        "event": "Severe Thunderstorm Warning",
        "severity": "Severe",
        "urgency": "Immediate",
        "certainty": "Observed",
        "headline": "Severe Thunderstorm Warning issued August 22 at 5:06PM CDT",
        "description": "At 505 PM CDT, a severe thunderstorm was located over Frayser.",
        "instruction": "For your protection move to an interior room on the lowest floor.",
        "effective": "2026-08-22T17:06:00-05:00",
        "expires": "2026-08-22T17:15:00-05:00"
      }
    }
  ]
}`

func TestFetchNOAAAlertsParsesRealShapedResponse(t *testing.T) {
	var gotQuery, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/geo+json")
		w.Write([]byte(noaaFixture))
	}))
	defer server.Close()
	originalBaseURL := noaaBaseURL
	noaaBaseURL = server.URL
	defer func() { noaaBaseURL = originalBaseURL }()

	alerts, err := FetchNOAAAlerts(context.Background(), 35.2, -90.05)
	if err != nil {
		t.Fatalf("FetchNOAAAlerts: %v", err)
	}
	if !strings.Contains(gotQuery, "point=35.2,-90.05") {
		t.Errorf("query = %q, want the point parameter", gotQuery)
	}
	if gotUserAgent == "" {
		t.Error("no User-Agent sent — weather.gov asks every client to identify itself")
	}
	if len(alerts) != 1 {
		t.Fatalf("alerts = %+v, want exactly 1", alerts)
	}
	a := alerts[0]
	if a.ID != "urn:oid:2.49.0.1.840.0.abc123.002.1" {
		t.Errorf("ID = %q", a.ID)
	}
	if a.Severity != SeveritySevere {
		t.Errorf("severity = %q, want severe", a.Severity)
	}
	if !strings.Contains(a.Title, "Severe Thunderstorm Warning") {
		t.Errorf("title = %q", a.Title)
	}
	if !strings.Contains(a.Body, "interior room") {
		t.Errorf("body = %q, want the instruction text included", a.Body)
	}
}

func TestFetchNOAAAlertsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	}))
	defer server.Close()
	originalBaseURL := noaaBaseURL
	noaaBaseURL = server.URL
	defer func() { noaaBaseURL = originalBaseURL }()

	alerts, err := FetchNOAAAlerts(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("FetchNOAAAlerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("alerts = %+v, want empty (e.g. a point outside US coverage)", alerts)
	}
}

func TestCheckNOAANotifiesOnlyNewAlerts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(noaaFixture))
	}))
	defer server.Close()
	originalBaseURL := noaaBaseURL
	noaaBaseURL = server.URL
	defer func() { noaaBaseURL = originalBaseURL }()

	notifier := &recordingNotifier{}
	seen, errs := CheckNOAA(context.Background(), 35.2, -90.05, map[string]bool{}, []Notifier{notifier})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %+v, want exactly 1 on first sighting", notifier.alerts)
	}
	if !seen["urn:oid:2.49.0.1.840.0.abc123.002.1"] {
		t.Error("returned seen set doesn't include the alert just seen")
	}

	// Second check, same still-active alert — must not notify again.
	_, errs = CheckNOAA(context.Background(), 35.2, -90.05, seen, []Notifier{notifier})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(notifier.alerts) != 1 {
		t.Errorf("alerts = %+v after a second check of the same active alert, want still 1", notifier.alerts)
	}
}

func TestNOAASeverityMapping(t *testing.T) {
	cases := map[string]Severity{
		"Extreme":  SeverityExtreme,
		"Severe":   SeveritySevere,
		"Moderate": SeverityWarning,
		"Minor":    SeverityWarning,
		"Unknown":  SeverityWarning,
	}
	for raw, want := range cases {
		if got := noaaSeverity(raw); got != want {
			t.Errorf("noaaSeverity(%q) = %q, want %q", raw, got, want)
		}
	}
}
