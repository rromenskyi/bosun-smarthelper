package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// WebhookNotifier POSTs an alert as JSON to any URL — the least
// opinionated channel, meant as the integration point for anything this
// package doesn't have a dedicated notifier for (Slack, Discord, ntfy,
// Home Assistant, a phone push service, ...), most of which accept a
// plain webhook themselves or via a small relay.
type WebhookNotifier struct {
	URL string
}

// webhookPayload is deliberately plain JSON, not any particular service's
// expected shape (e.g. Slack's {"text": ...}) — see docs/alerts.md for
// how to adapt it with a relay if the target needs a specific format.
type webhookPayload struct {
	Source   string `json:"source"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	At       string `json:"at"`
}

func (w *WebhookNotifier) Notify(ctx context.Context, alert Alert) error {
	body, err := json.Marshal(webhookPayload{
		Source:   alert.Source,
		Severity: string(alert.Severity),
		Title:    alert.Title,
		Body:     alert.Body,
		At:       alert.At.Format("2006-01-02T15:04:05Z07:00"),
	})
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
