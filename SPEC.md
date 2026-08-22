# Bosun (Старпом) — Specification

## Vision

Bosun (called «Старпом» in Russian) is a local-first AI assistant that works
**offline** (small local LLM) and
**online** (OpenAI-compatible remote LLM), with **MCP (Model Context
Protocol)** tools for real-world sensor access — outdoor temperature, fridge
temperature, GPS coordinates/speed, system status, and more as they're added.

**Language: Go.** Chosen for single-binary distribution, fast startup, native
concurrency for connectivity checks, and first-class stdio handling for MCP.

---

## Core Requirements

### 1. Dual-LLM Runtime

| Mode | Trigger | Model | Transport |
|------|---------|-------|-----------|
| **Offline** | No internet / remote unavailable | Local (Ollama) | HTTP to `localhost:11434` |
| **Online** | Internet available | Remote (OpenAI-compatible) | HTTPS |

- Automatic failover: remote call failure falls back to local mid-request.
- Endpoints, models, and timeouts are configurable via `config.yaml` / env vars.
- Both providers implement the same `llm.Client` interface (`internal/llm/types.go`).
- A fresh connectivity state also filters the tool definitions sent to the
  model. Network-dependent tools are not advertised in offline mode.

Status: **implemented** (`internal/llm/{types,local,remote,router}.go`) with
in-memory HTTP transport tests for Ollama and OpenAI-compatible wire formats.

### 2. MCP Tool Layer

Exposed over stdio as an MCP server (`smarthelper mcp`):

| Tool | Description | Example result |
|------|-------------|-----------------|
| `get_weather` | Current weather and 1–16 day forecast | `{"temperature_c": 22.5, "daily_forecast": [...]}` |
| `get_fridge_temp` | Fridge/freezer temperature | `{"fridge_c": 4.0, "freezer_c": -18.0}` |
| `get_gps` | Coordinates, speed, altitude | `{"latitude": 40.7608, "longitude": -111.891, "speed_kmh": 0}` |
| `get_system_info` | CPU (incl. temperature), RAM, disk, uptime | `{"cpu_percent": [...], "cpu_temp_c": 67, "memory": {...}}` |
| `memo` | Dated persistent local notes; `search` finds memos/documents by meaning; `topics` lists uploaded documents without searching; `maintenance` reports due equipment upkeep | `{"key":"shopping","updated_at":"...","age_days":2}` |
| `web_search` | DuckDuckGo results | `{"query":"...","results":[...]}` |
| `wikipedia` | Encyclopedia summary | `{"title":"...","extract":"...","url":"..."}` |
| `get_directions` | Google/Apple Maps links for a destination, routed from the current GPS location when available | `{"destination":"...","maps_url":"..."}` |

Status: **implemented**. All four tools have local/mock paths. Weather also has
an online Open-Meteo backend with Open-Meteo city geocoding, Nominatim landmark
fallback, and daily forecasts. GPS has a real serial/NMEA 0183 backend, tested
against an actual u-blox 7 USB receiver on this host — see
`internal/tools/gps_serial.go`. Fridge (1-Wire, MQTT) is not wired up yet.
Extensible: implement `tools.Tool` and register it.

### 3. Agent Rules

See `AGENTS.md`.

### 4. Project Structure

```
ai-local-smarthelper/
├── AGENTS.md
├── SPEC.md
├── README.md
├── LICENSE
├── Makefile
├── go.mod
├── .env.example
├── .gitignore
├── cmd/smarthelper/          # CLI entry point
├── internal/
│   ├── llm/                  # LLM abstraction (local + remote + router)
│   ├── mcp/                  # MCP server (stdio, JSON-RPC 2.0)
│   ├── tools/                # Built-in MCP tool implementations
│   └── config/               # Config loading (viper)
└── configs/
    └── config.yaml.example
```

### 5. Configuration (`config.yaml`)

See `configs/config.yaml.example` for the full annotated reference. Summary:

```yaml
llm:
  remote: {base_url, model, api_key_env, organization, timeout}
  local:  {base_url, model, timeout}
  router: {check_interval, check_timeout, check_target, prefer_remote}
mcp:
  server_name, transport, log_level
sensors:
  weather: {type: mock|open_meteo, default_location, timeout, ...}
  fridge:  {type: mock|mqtt, ...}
  gps:     {type: mock|serial, ...}
  system:  {type: native}
logging:
  level, format, output
```

### 6. Runtime Flow (target — see Roadmap)

```
User query (smarthelper chat)
    │
    ▼
Connectivity check (router.CheckConnectivity — configurable check_target)
    │
    ├─ online + remote configured ──▶ Remote LLM (OpenAI-compatible, configurable endpoint)
    │                                     │ request fails
    │                                     ▼
    └─ offline / remote unavailable ──▶ Local LLM (Ollama, local fallback)
                                              │
                                              ▼
                                agent.Agent loop: tool calls via registry
                                              │
                                              ▼
                                      Response to user
```

The router (local/remote selection with configurable endpoints and
connectivity-based fallback) and the MCP tool server are independent, tested
building blocks. `internal/agent` is the box that connects an LLM client to
the tool registry: it runs the loop — call the model, execute any tool calls
it returns, feed results back as `tool` messages, repeat until a final answer
or `maxToolIterations` is hit. `smarthelper chat "<message>"` runs one turn of
this loop through the router (remote-when-online, local-when-offline).

Before each turn, the agent obtains a fresh-enough connectivity state and
builds an availability-filtered tool contract. A tool whose configured
backend implements `tools.NetworkDependentTool` and returns `true` is omitted
while offline. The web UI applies the same available-tool list to its quick
actions. See `docs/offline-mode.md`.

The tool contract, system prompt, and chat history budget are all sized with
the weak local fallback model in mind — see `docs/token-budget.md` for
measured costs and the mitigations in place (compact tool rendering for
models without native tool calling, conservative default history limits).

### 7. MVP Scope (v0.1) — Status

- [x] Language decided: Go
- [x] Config loading + validation (viper, YAML + `SMARTHELPER_*` env overrides)
- [x] Local LLM client (Ollama HTTP API, `/api/chat`)
- [x] Remote LLM client (OpenAI-compatible `/chat/completions`), endpoint/model configurable
- [x] Router: connectivity check + local/remote selection + failover
- [x] Offline tool filtering for the LLM contract and web quick actions
- [x] MCP server (stdio, JSON-RPC 2.0): `initialize`, `tools/list`, `tools/call`
- [x] 4 built-in tools: `get_weather`, `get_fridge_temp`, `get_gps`, `get_system_info` (mock backends)
- [x] `internal/agent` — conversation loop wiring an LLM client to the tool registry
- [x] CLI: `smarthelper version`, `smarthelper mcp`, `smarthelper chat "<message>"`
- [x] LAN-only responsive web UI and JSON chat API (`smarthelper serve`)
- [x] Bounded multi-turn web sessions, persisted to disk (survive a page reload and a service restart)
- [x] Persistent dated memo tool with list/archive/delete lifecycle
- [x] DuckDuckGo and Wikipedia tools with offline filtering
- [x] Centralized tool/LLM failure log (`internal/errlog`) reviewable via `smarthelper errors`
- [x] `get_directions` tool: Google/Apple Maps links, asks for clarification instead of guessing an ambiguous destination
- [x] Configurable per-provider LLM temperature (remote higher, local lower — see `internal/config`)
- [x] Streamed web answers with a stop button; tool-call encodings folded, never shown raw — see `docs/streaming.md`
- [x] Docker + Compose deployment (`docs/docker.md`) — this host's live service runs from it, including two `llama-server` instances (chat + embeddings)
- [x] Semantic memo/document search (`docs/memo-search.md`) — reuses the `memo` tool's `search` action, so the LLM tool contract doesn't grow; document upload (text or PDF, web-UI-only) can attach a diagram image to a search result when a page has little or no text
- [x] Web UI settings page (`docs/settings.md`) — persona/prompt, default language, LLM temperatures, and memo canonical tags editable live from a JSON overlay store, no restart needed
- [x] Optional HTTPS via mkcert (`docs/tls.md`) — trusted-with-no-warning TLS for a private LAN IP that has no public CA
- [x] OCR for scanned PDF pages (tesseract, `eng` by default — `eng+rus` measurably misread English diagram text as look-alike Cyrillic garbage; per-upload `ocr_language` overrides it) — `internal/documents.CleanOCRText` strips residual noise, and `documents.Store.AttachOrphanedImages` (`smarthelper documents attach-images`) merges a diagram chunk onto the best-matching text chunk anywhere in the store instead of leaving it an orphaned, weakly-labeled entry
- [x] `make check` (fmt + vet + test + build) passing
- [x] Real serial/NMEA GPS backend (`internal/tools/gps_serial.go`) — tested against an actual u-blox 7 USB receiver; hot-pluggable (bind-mounted `/dev` + a cgroup device rule, not a static `devices:` entry) so bosun starts fine even if the GPS wasn't plugged in yet at boot
- [ ] Remaining real sensor backends (1-Wire temp probes, MQTT fridge, OBD2)
- [ ] Integration tests against a real Ollama instance
- [x] Push-to-talk voice interface (`docs/voice.md`) — whisper.cpp STT + Piper TTS, both directions, no cloud dependencies, no Python; continuous conversation mode still to come
- [x] Local monitoring dashboard (`docs/monitoring.md`) — a bounded-history SQLite time series for CPU/memory/disk/GPS/fridge, sampled on an interval; what to sample is a `config.yaml` list (`metrics.sources`), not hardcoded
- [x] Remote access via Cloudflare Tunnel (`docs/cloudflare.md`) — outbound-only `cloudflared`, a real Let's Encrypt cert via DNS-01, split-horizon DNS so the LAN path never leaves the network, and a Cloudflare Access gate in front of the tunnel — see the corrected non-goal below
- [x] Manual online/offline provider override (`internal/llm.Router.SetProviderOverride`) alongside the automatic connectivity-based selection, exposed as a clickable status pill in the web UI
- [x] `web.bind`/`http_fallback_bind` can be `0.0.0.0` (`webui.ValidateBind`), for a host without a DHCP reservation — still rejects public and link-local addresses; the no-auth LAN trust model is unchanged either way
- [x] Equipment maintenance tracking (`docs/maintenance-tracking.md`) — optional `metric_name`/`metric_value`/`due_date`/`due_metric_value` fields on a regular memo, domain-neutral (a car's odometer and a boat's main-engine hours both fit); `maintenance` action reports what's overdue/upcoming (computed in Go against the real clock) and the remaining counter value against the latest known reading; `write`'s own response flags a `metric_name` that doesn't match any other known one (`existing_metric_names`) so a weak model can self-correct within the same turn instead of fragmenting one counter into two names
- [x] Metric-merge approval queue (`internal/tools/memo_metric_merge.go`, `docs/maintenance-tracking.md`) — a periodic batched LLM check (plain text, not a tool call) proposes merging two `known_metrics` names that look like the same physical counter; nothing renames automatically — a human approves or rejects each suggestion in the web UI (🔗 icon, badge count via `GET`/`POST /api/metric-merges`), and a rejection is remembered so the same pair is never re-proposed

### 8. Local Web UI + Voice Interface

Target device is weak/low-power ("nano"-class embedded hardware) — the local
LLM must stay small (0.8B–2B parameter tier; already validated against real
hardware via `test-llm-0.8b.sh` / `test-llm-2b.sh` style smoke tests) for
usable latency offline.

- **Web server (implemented)**: serves a small UI reachable from a phone browser over the
  LAN. **No authentication** — trusted local network only, never exposed
  beyond it (no port-forwarding, no public bind address; bind to the LAN
  interface, not `0.0.0.0` on anything internet-facing).
- **Voice interface** (see `docs/voice.md`, **implemented**): whisper.cpp
  for speech-to-text (a press-and-hold 🎤 button, `POST /api/stt`,
  `internal/voice.WhisperCppSTT`) and Piper for spoken responses (a 🔊
  button on every assistant chat bubble, `POST /api/tts`,
  `internal/voice.PiperTTS`) — fully offline, no Python. A voice-in turn
  auto-speaks its own reply, closing the loop.
- **Language**: user-selectable, not hardcoded to English. Russian is the
  primary target language for STT/TTS (Whisper supports it natively; TTS
  engine choice must too).

The web UI is a client of the same conversation loop as `smarthelper chat`,
not a separate assistant implementation.

### 9. Non-Goals (for now)

- Multi-user / auth *for the base LAN service* (by design — no-auth,
  single trusted network; remote access is a separate, additive path —
  see below, not a change to this)
- Tool sandboxing (runs as the invoking user)
- Model management (assumes Ollama/llamafile already installed)

Internet-facing exposure was originally listed here as explicitly out of
scope; it's since been added deliberately (`docs/cloudflare.md`), but only
as an additive path gated by Cloudflare Access — the base LAN service is
still no-auth and was never made directly internet-facing itself.

---

## Roadmap (next after this foundation)

1. ~~Real backends for at least one sensor~~ — **done**: GPS now has a real
   serial/NMEA backend (`internal/tools/gps_serial.go`), tested against an
   actual USB receiver. Fridge (1-Wire `w1_slave` or MQTT) still uses `mock`.
2. Integration test tier (`tests/integration/`) that talks to a configured
   local model server when its endpoint is available, skipped otherwise.
3. ~~Voice interface: whisper.cpp STT + Piper TTS~~ — **shipped**,
   push-to-talk, both directions (see `docs/voice.md`). Continuous
   conversation mode (loop without pressing again) and a dedicated
   Bluetooth speaker are next.
4. ~~Remote access + a local monitoring dashboard~~ — **shipped**:
   Cloudflare Tunnel with a real cert and Access-gated auth
   (`docs/cloudflare.md`), and a config-driven metrics dashboard
   (`docs/monitoring.md`).
5. A document-navigation layer above flat chunk search — `memo`'s
   `topics`/`document_id` (list what's uploaded, scope a search to one
   document) are a first step; a fuller "chunkless RAG"-style structured
   navigation (walk a document's actual headings/sections instead of a
   flat similarity pool) is still just an idea, not started.
