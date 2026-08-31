# Settings page

The web UI has a settings dialog (gear icon, next to the documents icon)
for tweaking a handful of runtime knobs without editing `config.yaml` or
restarting the service:

- assistant name (`name_ru`/`name_en`) and system/style prompt
- default UI language (`ru`/`en`)
- LLM temperature, separately for the remote and local model
- the canonical tag vocabulary memo tag auto-normalization maps free-form
  memo tags onto (see `docs/memo-search.md`)
- dynamic topics: on by default (unlike every other toggle here — see
  "Why on by default" below) — adds a short line to the system prompt on
  every turn listing the distinct top-level filedump folders at least one
  RAG-ingested document lives under (e.g. `hunting-utah`, `ford`), nudging
  the model to check `memo` before answering from general knowledge or
  reaching for `web_search` on those topics. Nothing is added when there
  are no ingested documents yet, or when a document was never ingested
  from filedump at all (the older scripted import path falls back to its
  own title instead — see `internal/documents.Store.Topics`)
- automatic backup: on/off and how often (see `docs/backup.md`) — only
  shown once `backup.s3` is configured in `config.yaml`; the manual
  `smarthelper backup`/"back up now" button work regardless
- alert channels: a global on/off per channel for NOAA weather alerts,
  plus a full add/edit/remove list of metric threshold rules — bound,
  smoothing, its own per-rule channel selection, optional custom text and
  siren (see `docs/alerts.md`). A channel only appears once it's
  configured in `config.yaml`/`.env`; the "Алерты"/"Alerts" tab itself is
  hidden if no channel is configured at all

## Why a separate store, not `config.yaml`

`config.yaml` is hand-curated with inline comments explaining every knob
(units, trade-offs, defaults) — rewriting it from a web form would either
destroy those comments or require a full YAML-with-comments round-trip
library for little benefit. Instead, `internal/settings.Store`
(`internal/settings/store.go`) persists just the editable subset as JSON,
by default at `~/.local/share/bosun/settings.json` (`web.settings_store_path`
to override, e.g. for a Docker bind mount).

`config.yaml`'s values only **seed** the store the very first time it's
created. After that, the JSON file is authoritative — a later restart
loads whatever was last saved from the UI, not `config.yaml` again. To
reset a value, edit `settings.json` directly (or delete it to reseed from
`config.yaml` on next start) and restart.

## Why on by default

Every other toggle in this store defaults off — the established
convention is "opt in to a feature you asked for." Dynamic topics breaks
that on purpose: it's a nudge that only ever does anything once the
filedump actually has RAG-ingested content, and is harmless (one skipped
`if` in `buildMessages`, no prompt-token cost) when it doesn't — so
leaving it off by default would just mean it silently never engages for
someone who uploads reference material without separately remembering to
flip this on. `cmd/smarthelper/main.go`'s `settings.Load` seed defaults
set it `true`; this only takes effect for a brand-new `settings.json` —
an existing store keeps whatever the file already has (see "Why a
separate store, not `config.yaml`" above), so a deployment that predates
this feature needs it flipped on once from the settings page (or by
editing `settings.json` directly) to pick up the new default.

## Applying changes live

Every field applies immediately, without a restart:

- persona/style prompt → `agent.Agent.SetPersona` (already a live setter,
  used elsewhere for the same reason)
- dynamic topics → `agent.Agent.SetDynamicTopicsEnabled`, the same live-setter
  shape as persona; the actual topic list itself is never cached — it's
  read fresh from `internal/documents.Store.Topics` on every turn, so a
  newly uploaded file's folder shows up on the very next message, no
  restart or settings save needed for that part
- temperatures → `internal/llm.Router.SetTemperatures`, which forwards to
  new mutex-guarded `SetTemperature` methods on `RemoteClient`/`LocalClient`
  so an in-flight request finishes with whatever temperature it started
  with, and the next request picks up the new value
- default language → a mutex-guarded field on `webui.Server`
  (`SetDefaultLanguage`/`getDefaultLanguage`)
- canonical tags → `runTagNormalizer` (`cmd/smarthelper/main.go`) reads the
  current value from the settings store on every tick instead of a value
  captured once at startup
- backup schedule → `runBackupScheduler` (`cmd/smarthelper/main.go`) reads
  the current toggle/interval every tick the same way
- NOAA alert channels → `noaaAlertNotifiers` (`cmd/smarthelper/main.go`)
  re-reads which toggles are on every time an alert is about to fire,
  rather than a value captured once at startup
- threshold rules → `runThresholdChecker` (`cmd/smarthelper/main.go`)
  reads `settings.Data.AlertsThresholds` fresh on every 30s tick, so
  adding, editing, or removing a rule from the settings page takes effect
  on the very next tick

## API

- `GET /api/settings` → `{"enabled": false}` if no settings store is
  configured (shouldn't happen in normal operation — `main.go` always
  wires one up), otherwise `{"enabled": true, "settings": {...},
  "backup_configured": bool, "alerts_telegram_configured": bool,
  "alerts_webhook_configured": bool, "alerts_speaker_configured": bool}` —
  each `alerts_*_configured` flag is independent, matching whichever
  channels `config.yaml`/`.env` actually set up.
- `POST /api/settings` with a JSON body shaped like `Data`
  (`internal/settings/store.go`) **replaces** the stored settings wholesale
  — any field the body omits reverts to its Go zero value, since the
  handler decodes straight into a fresh `Data{}` rather than merging onto
  the existing one. The settings page's own form always resends every
  field together for exactly this reason; a different API client sending
  a partial body would zero out the rest. Persists, applies live, and
  returns the saved (normalized: trimmed, tags lowercased) result. Rejects
  out-of-range temperatures (`0`–`2`), unknown languages (must be `ru` or
  `en`), `backup_auto_enabled: true` with a non-positive
  `backup_interval_hours`, and an `alerts_thresholds` entry missing a
  `metric` or with an operator other than `>`, `<`, `>=`, `<=`, `==`, all
  with `400`. A threshold rule with no `id` gets one assigned server-side
  on save (`internal/settings.AlertsThresholdRule`) — the settings page
  never invents one itself, and the LLM never sees or sets it at all
  (`internal/tools.CodeExecTool`'s sibling, `run_code`'s `session_id`,
  follows the same "never let the caller supply an identity used for
  storage/state" rule).
- `GET /api/backups` → `{"configured": false, "backups": []}` if
  `backup.s3` isn't set in `config.yaml`, otherwise
  `{"configured": true, "backups": [{"key", "size_bytes", "last_modified"}, ...]}`.
- `POST /api/backups` → runs one backup immediately (same as
  `smarthelper backup`) and records it the same way an automatic run
  would, resetting the schedule's countdown. `501` if unconfigured.

If the settings page's gear icon doesn't appear, the frontend hid it
because `GET /api/settings` reported `enabled: false` — the same pattern
the documents icon uses (see `docs/memo-search.md`).
