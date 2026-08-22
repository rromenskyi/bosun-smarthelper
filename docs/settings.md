# Settings page

The web UI has a settings dialog (gear icon, next to the documents icon)
for tweaking a handful of runtime knobs without editing `config.yaml` or
restarting the service:

- assistant name (`name_ru`/`name_en`) and system/style prompt
- default UI language (`ru`/`en`)
- LLM temperature, separately for the remote and local model
- the canonical tag vocabulary memo tag auto-normalization maps free-form
  memo tags onto (see `docs/memo-search.md`)
- automatic backup: on/off and how often (see `docs/backup.md`) — only
  shown once `backup.s3` is configured in `config.yaml`; the manual
  `smarthelper backup`/"back up now" button work regardless
- alert channels: on/off, one toggle per channel (see `docs/alerts.md`) —
  a channel only appears once it's configured in `config.yaml`/`.env`; the
  "Алерты"/"Alerts" tab itself is hidden if no channel is configured at all

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

## Applying changes live

Every field applies immediately, without a restart:

- persona/style prompt → `agent.Agent.SetPersona` (already a live setter,
  used elsewhere for the same reason)
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
- alert channels → `activeAlertNotifiers` (`cmd/smarthelper/main.go`)
  re-reads which toggles are on every time an alert is about to fire,
  rather than a value captured once at startup

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
  `en`), and `backup_auto_enabled: true` with a non-positive
  `backup_interval_hours`, all with `400`.
- `GET /api/backups` → `{"configured": false, "backups": []}` if
  `backup.s3` isn't set in `config.yaml`, otherwise
  `{"configured": true, "backups": [{"key", "size_bytes", "last_modified"}, ...]}`.
- `POST /api/backups` → runs one backup immediately (same as
  `smarthelper backup`) and records it the same way an automatic run
  would, resetting the schedule's countdown. `501` if unconfigured.

If the settings page's gear icon doesn't appear, the frontend hid it
because `GET /api/settings` reported `enabled: false` — the same pattern
the documents icon uses (see `docs/memo-search.md`).
