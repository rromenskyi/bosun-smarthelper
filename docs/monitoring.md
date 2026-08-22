# Local monitoring dashboard

A personal, bounded-history analog to MRTG/Grafana: sample a handful of
sensors on an interval, keep a fixed-size history, chart them from a button
in the web UI (📊). No external service, no network dependency — a single
local SQLite file and vanilla-JS canvas charts, matching the rest of this
project's offline-first, zero-extra-dependency web UI.

## Architecture

- **`internal/metrics.Store`** — a SQLite file (`modernc.org/sqlite`, a
  pure-Go driver — no cgo, which matters on this host after the
  Alpine/musl vs glibc onnxruntime saga documented in `docs/voice.md`;
  the same class of problem was worth avoiding here from the start).
  One table, `samples(ts, metric, value)`, indexed on `(metric, ts)`.
- **`internal/metrics.Collector`** — a goroutine that samples every
  `metrics.interval` (default 30s) and prunes anything older than
  `metrics.retention_days` (default 30) once an hour — the retention is
  what keeps this MRTG-like (bounded size) instead of growing forever.
- **`GET /api/metrics/list`** / **`GET /api/metrics?metric=X&range=Y`** —
  `internal/webui/metrics.go`. The list endpoint only reports metrics that
  actually have at least one sample, so the dashboard's checklist never
  offers a metric for hardware that isn't wired up yet. Wide ranges get
  server-side bucketing/averaging (`Store.Query`'s `maxPoints`) so a 30-day
  chart isn't dragging tens of thousands of raw points over the wire.
- **The 📊 button** (`internal/webui/index.html`) — hidden unless
  `GET /api/status` reports `metrics_enabled: true`. Opens a dialog with a
  range picker (1h/24h/7d/30d), a metric checklist, and one small
  hand-rolled canvas line chart per selected metric — no charting library
  vendored; there are only a handful of metrics and the rendering needed
  (line + min/mid/max labels + two time labels) is simple enough that
  pulling in a dependency (even a small embedded one) wasn't worth it.
  Refreshes every 30s while open.

## What to sample is config, not code

`config.yaml`'s `metrics.sources` is a list of `{metric, tool, args, field,
aggregate, label_ru, label_en, unit}` — see `configs/config.yaml.example`
for the full shape and defaults. `tool` is a name from the same
`tools.Registry` the chat agent already uses, so a sensor is implemented
once and reused for both chat answers and the dashboard; `field` is a
dot-separated path into that tool's JSON-ish result (e.g.
`"memory.used_percent"`, `"cpu.used_percent"`); `aggregate: avg` handles a
field that's a `[]float64` (a genuinely per-core reading, not currently
used by any shipped source — `get_system_info`'s own `cpu.used_percent` is
already a single aggregate number) by averaging it into one.

This means **adding a new sensor to the dashboard once its tool exists is a
config.yaml edit, not a Go change** — e.g. once a battery or water-tank
tool is implemented:

```yaml
metrics:
  sources:
    - metric: battery_percent
      tool: get_battery
      field: percent
      label_ru: "Заряд"
      label_en: "Charge"
      unit: "%"
```

The default source list (`internal/config`'s `setDefaults`) covers every
sensor tool this project ships with today: `cpu_temp_c`, `cpu_percent`,
`mem_used_percent`, `disk_used_percent` (all from `get_system_info`),
`gps_speed_kmh` (`get_gps`), and `fridge_c`/`freezer_c` (`get_fridge_temp`).
Setting `metrics.sources` in `config.yaml` replaces this list entirely
(standard Viper behavior for a slice) — copy the defaults from the example
file first if you just want to add one more source rather than starting
from zero.

## A tool erroring for one tick isn't a failure

GPS with no fix yet, a fridge sensor briefly unreachable — `sampleAll`
(`internal/metrics/collector.go`) just skips that source's metric for this
tick and tries again next time. Sources sharing a tool+args pair (e.g.
`cpu_temp_c` and `cpu_percent` both come from one `get_system_info` call
with `{"include":["cpu"]}`) are deduplicated within a single tick so the
same sensor read isn't paid for twice.
