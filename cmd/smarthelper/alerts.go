package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/alerts"
	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/metrics"
	"github.com/roman220/bosun-smarthelper/internal/settings"
	"github.com/roman220/bosun-smarthelper/internal/tools"
	"github.com/roman220/bosun-smarthelper/internal/voice"
)

// telegramNotifier/webhookNotifier/speakerNotifier each build one channel
// if it's both configured (config.yaml/.env) and enabled by the caller —
// shared by noaaAlertNotifiers (one global enabled flag per channel) and
// thresholdRuleNotifiers (one enabled flag per rule, per channel). Return
// a bare `nil` (not a typed nil pointer) on every "not applicable" path,
// so the caller's `!= nil` check against the alerts.Notifier interface
// behaves correctly.
func telegramNotifier(cfg config.AlertsTelegramConfig, enabled bool, logger *slog.Logger) alerts.Notifier {
	if cfg.ChatID == "" || !enabled {
		return nil
	}
	botToken := os.Getenv(cfg.BotTokenEnv)
	if botToken == "" {
		logger.Warn("telegram alerts enabled but bot token env var is empty", "env", cfg.BotTokenEnv)
		return nil
	}
	return &alerts.TelegramNotifier{BotToken: botToken, ChatID: cfg.ChatID}
}

func webhookNotifier(cfg config.AlertsWebhookConfig, enabled bool) alerts.Notifier {
	if cfg.URL == "" || !enabled {
		return nil
	}
	return &alerts.WebhookNotifier{URL: cfg.URL}
}

func speakerNotifier(cfg config.AlertsSpeakerConfig, enabled bool, ttsEngine voice.TTSEngine, language string, logger *slog.Logger) alerts.Notifier {
	if !cfg.Enabled || !enabled {
		return nil
	}
	if ttsEngine == nil {
		logger.Warn("speaker alerts enabled but no TTS engine is configured (voice.tts.model_path)")
		return nil
	}
	return &alerts.SpeakerNotifier{TTS: ttsEngine, PlayerPath: cfg.PlayerPath, Language: language}
}

// sendTestAlert delivers one harmless, clearly-marked test notification
// through a single named channel — the settings page's "test" button
// (docs/alerts.md), the only way to find out a bot token is wrong, a
// webhook URL is unreachable, or the speaker channel has no working audio
// device *before* a real NOAA alert or threshold crossing silently fails
// to reach anyone. Passes enabled: true to the notifier constructor
// regardless of that channel's own settings-page toggle — being off is
// exactly the state a human tests from before deciding to flip it on.
func sendTestAlert(ctx context.Context, cfg *config.Config, ttsEngine voice.TTSEngine, language string, logger *slog.Logger, channel string) error {
	var notifier alerts.Notifier
	switch channel {
	case "telegram":
		notifier = telegramNotifier(cfg.Alerts.Channels.Telegram, true, logger)
	case "webhook":
		notifier = webhookNotifier(cfg.Alerts.Channels.Webhook, true)
	case "speaker":
		notifier = speakerNotifier(cfg.Alerts.Channels.Speaker, true, ttsEngine, language, logger)
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
	if notifier == nil {
		return fmt.Errorf("channel %q is not configured", channel)
	}
	return notifier.Notify(ctx, alerts.Alert{
		Source:   "test",
		Severity: alerts.SeverityInfo,
		Title:    "Bosun test alert",
		Body:     "This is a test from the settings page — no actual emergency.",
		At:       time.Now(),
	})
}

func collectNotifiers(candidates ...alerts.Notifier) []alerts.Notifier {
	var notifiers []alerts.Notifier
	for _, n := range candidates {
		if n != nil {
			notifiers = append(notifiers, n)
		}
	}
	return notifiers
}

// noaaAlertNotifiers assembles every channel that's both configured
// (config.yaml/.env) and globally enabled (the settings page's NOAA
// toggles) — NOAA is a single source, so unlike threshold rules there's
// no per-rule channel selection to make, just one on/off per channel.
// Re-read on every check rather than cached once, so flipping a settings
// toggle takes effect on the very next tick, not after a restart.
func noaaAlertNotifiers(cfg *config.Config, settingsStore *settings.Store, ttsEngine voice.TTSEngine, logger *slog.Logger) []alerts.Notifier {
	data := settingsStore.Get()
	return collectNotifiers(
		telegramNotifier(cfg.Alerts.Channels.Telegram, data.AlertsTelegramEnabled, logger),
		webhookNotifier(cfg.Alerts.Channels.Webhook, data.AlertsWebhookEnabled),
		speakerNotifier(cfg.Alerts.Channels.Speaker, data.AlertsSpeakerEnabled, ttsEngine, data.DefaultLanguage, logger),
	)
}

// thresholdRuleNotifiers assembles the channels one specific web-managed
// threshold rule (settings.AlertsThresholdRule) has opted into — a
// channel still only fires if it's also configured in
// config.yaml/.env, same "config decides what exists" rule as
// noaaAlertNotifiers, just keyed off the rule's own checkboxes instead of
// a single global toggle.
func thresholdRuleNotifiers(cfg *config.Config, rule settings.AlertsThresholdRule, ttsEngine voice.TTSEngine, language string, logger *slog.Logger) []alerts.Notifier {
	return collectNotifiers(
		telegramNotifier(cfg.Alerts.Channels.Telegram, rule.Telegram, logger),
		webhookNotifier(cfg.Alerts.Channels.Webhook, rule.Webhook),
		speakerNotifier(cfg.Alerts.Channels.Speaker, rule.Speaker, ttsEngine, language, logger),
	)
}

// currentPosition resolves the point NOAA alerts should watch: a fixed
// config.yaml lat/lon, or — with use_gps — whatever the get_gps tool
// reports right now, the point that actually matters for a vehicle that
// moves rather than a fixed value that's only ever right by luck.
func currentPosition(ctx context.Context, registry *tools.Registry, noaaCfg config.AlertsNOAAConfig) (float64, float64, error) {
	if !noaaCfg.UseGPS {
		return noaaCfg.Latitude, noaaCfg.Longitude, nil
	}
	gpsTool, ok := registry.Get("get_gps")
	if !ok {
		return 0, 0, fmt.Errorf("alerts.noaa.use_gps is set but no get_gps tool is registered")
	}
	result, err := gpsTool.Execute(ctx, map[string]any{})
	if err != nil {
		return 0, 0, fmt.Errorf("read GPS position: %w", err)
	}
	data, ok := result.(map[string]any)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected get_gps result shape: %T", result)
	}
	lat, ok := data["latitude"].(float64)
	if !ok {
		return 0, 0, fmt.Errorf("get_gps result missing numeric latitude")
	}
	lon, ok := data["longitude"].(float64)
	if !ok {
		return 0, 0, fmt.Errorf("get_gps result missing numeric longitude")
	}
	return lat, lon, nil
}

// seedThresholdRules converts config.yaml's alerts.thresholds (if any)
// into the settings-managed rule shape, for settings.Load to seed the
// very first time — a one-time migration path, not something read again
// after that (see runThresholdChecker). Every channel starts off so the
// human opts each rule into Telegram/webhook/speaker from the settings
// page explicitly, same as config.yaml only ever provided metric/
// operator/value/title before this feature existed.
func seedThresholdRules(configured []config.AlertsThresholdConfig) []settings.AlertsThresholdRule {
	rules := make([]settings.AlertsThresholdRule, 0, len(configured))
	for _, t := range configured {
		rules = append(rules, settings.AlertsThresholdRule{
			Metric: t.Metric, Operator: t.Operator, Value: t.Value, Title: t.Title,
			SmoothingSamples: 1,
		})
	}
	return rules
}

// runThresholdChecker watches internal/settings.Data.AlertsThresholds —
// web-managed rules, not config.yaml (which only ever seeds that list
// once, see settings.Load's call site above) — against internal/metrics'
// samples, notifying only on a state transition (see
// alerts.ThresholdChecker.Check). Rules are re-read from settingsStore on
// every tick, so adding, editing, or removing one from the settings page
// takes effect on the very next tick, not after a restart — every metric
// name here is exactly whatever metrics.sources already samples, so a
// future custom sensor (a tank level, battery charge) needs no change in
// this function, only a new metrics.sources entry and a new rule from
// the web UI.
func runThresholdChecker(
	ctx context.Context,
	cfg *config.Config,
	settingsStore *settings.Store,
	metricsStore *metrics.Store,
	ttsEngine voice.TTSEngine,
	dataDir string,
	logger *slog.Logger,
) {
	state, err := alerts.LoadThresholdState(dataDir)
	if err != nil {
		logger.Warn("load threshold alert state; starting fresh", "error", err)
		state = map[string]bool{}
	}

	const checkInterval = 30 * time.Second
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data := settingsStore.Get()
			thresholds := make([]alerts.Threshold, 0, len(data.AlertsThresholds))
			for _, rule := range data.AlertsThresholds {
				thresholds = append(thresholds, alerts.Threshold{
					ID:               rule.ID,
					Metric:           rule.Metric,
					Operator:         rule.Operator,
					Value:            rule.Value,
					Title:            rule.Title,
					SmoothingSamples: rule.SmoothingSamples,
					CustomText:       rule.CustomText,
					PlaySiren:        rule.Siren,
					Notifiers:        thresholdRuleNotifiers(cfg, rule, ttsEngine, data.DefaultLanguage, logger),
				})
			}
			checker := alerts.ThresholdChecker{Store: metricsStore, Thresholds: thresholds}
			next, errs := checker.Check(ctx, state)
			for _, err := range errs {
				logger.Warn("threshold alert check", "error", err)
			}
			state = next
			if err := alerts.SaveThresholdState(dataDir, state); err != nil {
				logger.Warn("save threshold alert state", "error", err)
			}
		}
	}
}

// runNOAAChecker polls weather.gov for active alerts covering the current
// position (see currentPosition) and notifies about every one not already
// seen (alerts.CheckNOAA) — US coverage only; a point outside it just
// means an empty result every tick, not an error.
func runNOAAChecker(
	ctx context.Context,
	cfg *config.Config,
	registry *tools.Registry,
	settingsStore *settings.Store,
	ttsEngine voice.TTSEngine,
	dataDir string,
	logger *slog.Logger,
) {
	seen, err := alerts.LoadNOAASeenIDs(dataDir)
	if err != nil {
		logger.Warn("load NOAA alert state; starting fresh", "error", err)
		seen = map[string]bool{}
	}

	interval, err := time.ParseDuration(cfg.Alerts.NOAA.CheckInterval)
	if err != nil || interval <= 0 {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lat, lon, err := currentPosition(ctx, registry, cfg.Alerts.NOAA)
			if err != nil {
				logger.Warn("resolve position for NOAA alerts", "error", err)
				continue
			}
			notifiers := noaaAlertNotifiers(cfg, settingsStore, ttsEngine, logger)
			next, errs := alerts.CheckNOAA(ctx, lat, lon, seen, notifiers)
			for _, err := range errs {
				logger.Warn("NOAA alert check", "error", err)
			}
			seen = next
			if err := alerts.SaveNOAASeenIDs(dataDir, seen); err != nil {
				logger.Warn("save NOAA alert state", "error", err)
			}
		}
	}
}
