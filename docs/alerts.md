# Alerts

A small, deliberately narrow notification system for "something is wrong
enough a human should hear about it right now, wherever they are" —
not a general logging or pub/sub system. Two sources, three channels.

## Sources

- **NOAA weather alerts** (`internal/alerts/noaa.go`) — polls
  `api.weather.gov/alerts/active?point=lat,lon`, no API key, US territory
  only (a point outside it just returns no alerts, not an error). The
  point checked is either a fixed `latitude`/`longitude` in `config.yaml`,
  or — with `use_gps: true` — whatever the `get_gps` tool reports on every
  tick, which is the right choice for anything that moves.
- **Metric thresholds** (`internal/alerts/threshold.go`) — watches any
  metric `internal/metrics` already samples (`metrics.sources` in
  `config.yaml`, see `docs/monitoring.md`) against a bound. This package
  has no idea what a metric physically represents — a future
  battery-charge or grey/black/fresh-water-tank sensor needs only a new
  `metrics.sources` entry, no code change here. Unlike NOAA, threshold
  *rules* are entirely web-managed (added/edited/removed from the
  settings page, not `config.yaml`) — see "Threshold rules" below.

Both are **edge-triggered**: a threshold notifies once when it crosses,
once more when it goes back to normal, never again on every tick while it
stays crossed. NOAA notifies once per alert ID, never again for the same
still-active alert. State survives a restart (`alerts_threshold_state.json`,
`alerts_noaa_seen_state.json` in the data directory) — see "Why separate
state files" below.

**At-most-once, not guaranteed delivery, by design**: state flips to
"notified" as soon as a check runs, regardless of whether any configured
notifier actually succeeded (`internal/alerts.ThresholdChecker.Check`,
`CheckNOAA`). If Telegram/the webhook endpoint is briefly unreachable
right when a rule crosses, that one delivery is lost — it does *not*
retry on the next tick, since by then the state already reads "already
notified" and nothing has changed. The alternative (only commit "notified"
once a notifier succeeds) trades this for a worse failure mode: a
permanently broken channel would make the checker retry-storm forever on
every tick for a metric that's genuinely still crossed. Given the
channels here (Telegram, a webhook, the local speaker) are all either
reliable or fail in a way a human will notice on their own (a broken
webhook, an unplugged speaker), the simpler at-most-once behavior was
kept — flagged here explicitly since it's a real trade-off, not an
oversight.

## Config

```yaml
alerts:
  noaa:
    use_gps: true            # or a fixed point:
    # latitude: 42.35
    # longitude: -71.05
    check_interval: "15m"
  thresholds:                # optional — see "Threshold rules" below
    - metric: disk_used_percent
      operator: ">"           # ">", "<", ">=", "<=", "=="
      value: 90
      title: "Disk space"
  channels:
    telegram:
      bot_token_env: "ALERTS_TELEGRAM_BOT_TOKEN"  # set in .env
      chat_id: "123456789"
    webhook:
      url: "https://example.com/bosun-alerts"
    speaker:
      enabled: true
      player_path: "aplay"
```

`noaa` with neither `use_gps` nor a non-zero `latitude`/`longitude`
disables the NOAA checker (no goroutine started). The threshold checker
is always started once metrics are enabled (`metrics.enabled`, the
default) — it's a no-op on every tick until at least one rule exists, and
rules now live in the settings page, not `config.yaml`.

## Threshold rules — web-managed, not `config.yaml`

`alerts.thresholds` in `config.yaml` (above) is a **one-time seed**, the
same "config.yaml seeds it once, the settings page is authoritative after
that" pattern every other editable setting uses (`docs/settings.md`).
After the very first run, rules are added, edited, and removed entirely
from the settings page's "Алерты"/"Alerts" tab — a "Пороговые алерты"/
"Threshold alerts" section lists them, with an "+ Добавить"/"+ Add"
button for a new one. Each rule picks:

- **Metric** — a dropdown populated from `GET /api/metrics/list`, i.e.
  exactly whatever `metrics.sources` already reports (`docs/monitoring.md`).
- **Bound** — an operator (`>`, `<`, `>=`, `<=`, `==`) and a value.
- **Smoothing** — compares a moving average of the last N raw samples
  instead of the single latest reading, to reduce false alarms from a
  noisy sensor. N = 1 (the default) means no smoothing.
- **Channels** — its own independent Telegram/webhook/speaker checkboxes.
  Unlike NOAA (one source, one global toggle per channel — see below),
  each threshold rule chooses its own subset of whichever channels are
  configured; a checkbox for a channel with no credentials in
  `config.yaml`/`.env` simply doesn't appear.
- **Custom text** (optional) — replaces the auto-generated alarm message
  ("*metric* is *value* (threshold: *op* *value*)") sent to every channel
  this rule has enabled. Never applied to the "back to normal" recovery
  message. Works identically across channels — Telegram just sends this
  as its plain-text message, the webhook's JSON shape
  (`source`/`severity`/`title`/`body`/`at`) doesn't change based on what
  string fills `body`.
- **Siren** (speaker only) — plays a short built-in sound
  (`internal/alerts/assets/siren.wav`, embedded in the binary) before the
  spoken text. No config path, no upload — one signal is enough to mean
  "pay attention."

Two rules can watch the same metric (e.g. a low-battery rule and a
high-battery rule) — each is tracked independently
(`internal/alerts.Threshold.ID`, generated server-side, never something
the settings page or the LLM invents itself).

## Channels

A channel only actually fires once it's both **configured here** (or in
`.env`) and **enabled** — the same "config decides what exists, settings
decides what's live" split `backup.s3`/the auto-backup toggle already
uses. What "enabled" means depends on the source: NOAA has one global
on/off per channel (there's only one NOAA source); each threshold rule
has its own independent per-channel checkboxes (see above). Either way, a
channel with no config at all doesn't show up on the settings page as an
option to enable.

- **Telegram** (`internal/alerts/telegram.go`) — plain text via a bot's
  `sendMessage`. Create a bot with
  [@BotFather](https://core.telegram.org/bots#botfather), add it to the
  chat you want alerts in, find that chat's numeric ID (e.g. via
  `getUpdates` on the bot's own API), put the ID in `chat_id` and the bot
  token in `.env` as `ALERTS_TELEGRAM_BOT_TOKEN`. Works from anywhere the
  host has internet access — no port forwarding, no paired device.
- **Webhook** (`internal/alerts/webhook.go`) — POSTs a plain JSON body
  (`{"source", "severity", "title", "body", "at"}`) to any URL. Not
  shaped for a specific service (Slack, Discord, ntfy, Home Assistant,
  a phone push service, ...) — most accept a plain webhook directly, or
  via a small relay if they need their own shape.
- **Speaker** (`internal/alerts/speaker.go`) — synthesizes the alert
  through the same Piper TTS engine regular voice replies use
  (`voice.tts` must be configured), then plays the resulting WAV by
  shelling out to `player_path` (`aplay` by default, reading from stdin).
  The one channel that reaches someone without a phone in hand — asleep
  belowdecks, say.

### Testing a channel before trusting it

Every configured channel gets a "Test"/"Тест" button next to it on the
settings page (`POST /api/alerts/test {"channel": "telegram"|"webhook"|
"speaker"}`, `handleAlertsTest` in `internal/webui/alerts.go`) that fires
one real, clearly-marked test notification through it — a wrong bot
token, an unreachable webhook URL, or a speaker channel with no working
audio device otherwise all fail exactly the same way a real alert would:
silently, per the at-most-once section above. Deliberately ignores that
channel's own settings-page enabled toggle, since testing is how a human
decides whether to flip that toggle on in the first place. Returns the
real underlying error on failure (a wrong chat ID, a connection refused)
rather than swallowing it, since debugging *why* a channel doesn't work
is the entire point of this button.

### Docker: reaching real audio hardware

`docker-compose.yml` already bind-mounts the host's whole `/dev` into the
container (for the GPS, `internal/tools/gps_serial.go`), so `/dev/snd`'s
device nodes are visible — but a plain bind mount doesn't grant the
actual open()/read()/write() syscalls; Docker's cgroup device controller
denies everything not explicitly listed. `docker-compose.yml` adds
`device_cgroup_rules: - "c 116:* rmw"` (116 = this host's ALSA major,
`ls -la /dev/snd`) and `group_add: - "29"` (this host's `audio` GID, since
`/dev/snd/*` is owned `root:audio` mode `660`) for exactly this. The
runtime image installs `alsa-utils` for `aplay` (`Dockerfile`). If either
the audio major or the `audio` GID differs on your host, update both to
match. Without this, `SpeakerNotifier.Notify` fails with a real error
(visible in the container logs) rather than silently doing nothing —
there's no permission to reach the audio device from inside the
container.

## Notification zone — a persisted record, not just a one-time delivery

Every channel above delivers an alert once and is done with it — a NOAA
warning spoken through the speaker, or a threshold crossing sent to
Telegram, leaves no record inside the app itself once it's fired.
`internal/notifications` fixes that: `notificationStoreNotifier`
(`cmd/smarthelper/alerts.go`) is added to both `thresholdRuleNotifiers`
and `noaaAlertNotifiers` unconditionally, alongside whichever real
channels a rule opted into, so every alert that fires is recorded
regardless of Telegram/webhook/speaker configuration. It's not itself one
of the opt-in checkboxes — there's no "notification zone" toggle to miss.

Persisted as a capped JSON file (`notifications.json`, 200 most-recent
entries, oldest dropped first — same rotation reasoning as
`internal/errlog`), read by the web UI's ⚠️ header toggle: a badge shows
the unread count, the dialog lists title/body/time for each, click one to
mark it read or ✕ to dismiss it entirely. `GET /api/notifications` (list
+ unread count), `POST /api/notifications/read` (`{"id"}` or
`{"all": true}`), `DELETE /api/notifications?id=` — all report
`enabled: false` rather than erroring when no store is configured, the
same shape as the other optional web UI features.

### Beyond threshold/NOAA alerts: background job completions and failures

The zone also records two other categories, on the reasoning that
something is "important" here if it either (a) has *no other visibility*
in the web UI at all, or (b) genuinely indicates a malfunction or
data-loss risk rather than routine/expected state (a GPS tool reporting
"no fix yet" doesn't qualify; a scheduled backup failing to upload does):

- **Filedump RAG ingestion** (`internal/webui/filedump.go`'s
  `notifyFileDumpIngest`) — a background upload's completion, success or
  failure, previously only showed as a badge in the file browser someone
  had to think to go check. One notification per real upload; no flood
  risk since uploads aren't a recurring tick.
- **Background scheduler failures** — `runTagNormalizer`,
  `runMetricMergeChecker`, `runBackupScheduler`
  (`cmd/smarthelper/background.go`), and the threshold/NOAA checkers'
  *own* infrastructure failures (loading/saving state, resolving
  position) as opposed to the alerts they detect. A completed *scheduled*
  backup also gets a plain info notification — the one bit of "did it
  actually work" confirmation beyond checking S3 directly. Routine
  per-turn tool/LLM errors are deliberately excluded: the user already
  sees those live in the conversation the moment they happen, so
  duplicating them into the zone would just be noise.

Every one of these recurring-ticker failures goes through
`notifications.Store.AddDeduped` (`notificationDedupWindow`, currently
1 hour) rather than plain `Add` — a threshold check ticks every 30s, a
backup schedule check every 15 minutes, so a persistent failure (a dead
embeddings server, bad S3 credentials) would otherwise flood the zone
with an identical entry every single tick instead of the one the user
actually needs to see. The first occurrence always gets through
immediately; a genuinely different failure (different title) is never
suppressed regardless of the window.

## Settings page

Once a channel is configured, the settings page's "Алерты"/"Alerts" tab
shows a global toggle for it (NOAA) — off by default, same as every other
opt-in background pass in this project — and makes it available as a
per-rule checkbox for threshold rules (see above). See `docs/settings.md`.

## Why separate state files, not `settings.Store`

`settings.Store`'s `POST /api/settings` is a full replace of the whole
JSON blob, not a merge (see `docs/settings.md`) — folding the
threshold-crossed map or NOAA's seen-alert-ID set into it would mean an
unrelated settings save (e.g. changing the assistant's name) silently
zeroes out that bookkeeping. Both live in their own small atomically
written JSON files in the data directory instead, the same pattern
`internal/backup/schedule.go` already uses for its own last-run timestamp.
