# ai-local-smarthelper — Specification

## Vision

A local-first AI assistant that works **offline** (small local LLM) and
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

Status: **implemented** (`internal/llm/{types,local,remote,router}.go`), unit-testable
via mock HTTP servers (not yet added — see Roadmap).

### 2. MCP Tool Layer

Exposed over stdio as an MCP server (`smarthelper mcp`):

| Tool | Description | Example result |
|------|-------------|-----------------|
| `get_weather` | Outdoor temperature/humidity | `{"temperature_c": 22.5, "humidity": 60}` |
| `get_fridge_temp` | Fridge/freezer temperature | `{"fridge_c": 4.0, "freezer_c": -18.0}` |
| `get_gps` | Coordinates, speed, altitude | `{"latitude": 55.75, "longitude": 37.61, "speed_kmh": 0}` |
| `get_system_info` | CPU, RAM, disk, uptime | `{"cpu_percent": [...], "memory": {...}}` |

Status: **implemented**, all four tools currently backed by `mock` config
(fixed/configurable values) — real sensor backends (1-Wire, MQTT, serial GPS)
are not wired up yet. Extensible: implement `tools.Tool` and register it.

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
  weather: {type: mock|mqtt|http|1wire, ...}
  fridge:  {type: mock|mqtt, ...}
  gps:     {type: mock|serial, ...}
  system:  {type: native}
logging:
  level, format, output
```

### 6. Runtime Flow (target — see Roadmap)

```
User query
    │
    ▼
Connectivity check (router.CheckConnectivity)
    │
    ├─ online + remote configured ──▶ Remote LLM (OpenAI-compatible)
    │                                     │ request fails
    │                                     ▼
    └─ offline / remote unavailable ──▶ Local LLM (Ollama)
                                              │
                                              ▼
                                   Tool calls via MCP registry
                                              │
                                              ▼
                                      Response to user
```

Today, the router and the MCP tool server are independent, tested building
blocks. The box that connects them — an actual conversation loop that takes a
user message, calls the LLM with tool definitions, executes any tool calls,
and feeds results back — does not exist yet. That's `smarthelper chat`.

### 7. MVP Scope (v0.1) — Status

- [x] Language decided: Go
- [x] Config loading + validation (viper, YAML + `SMARTHELPER_*` env overrides)
- [x] Local LLM client (Ollama HTTP API, `/api/chat`)
- [x] Remote LLM client (OpenAI-compatible `/chat/completions`)
- [x] Router: connectivity check + local/remote selection + failover
- [x] MCP server (stdio, JSON-RPC 2.0): `initialize`, `tools/list`, `tools/call`
- [x] 4 built-in tools: `get_weather`, `get_fridge_temp`, `get_gps`, `get_system_info` (mock backends)
- [x] CLI: `smarthelper version`, `smarthelper mcp`
- [x] `make check` (fmt + vet + test + build) passing
- [ ] `smarthelper chat` — agent loop wiring LLM ⇄ tools together
- [ ] Real sensor backends (1-Wire temp probes, MQTT, serial GPS/OBD2)
- [ ] Integration tests against a real Ollama instance

### 8. Local Web UI + Voice Interface (planned)

Target device is weak/low-power ("nano"-class embedded hardware) — the local
LLM must stay small (0.8B–2B parameter tier; already validated against real
hardware via `test-llm-0.8b.sh` / `test-llm-2b.sh` style smoke tests) for
usable latency offline.

- **Web server**: serves a small UI reachable from a phone browser over the
  LAN. **No authentication** — trusted local network only, never exposed
  beyond it (no port-forwarding, no public bind address; bind to the LAN
  interface, not `0.0.0.0` on anything internet-facing).
- **Voice interface**: Whisper for speech-to-text, a TTS engine for spoken
  responses — "hey, what's the weather like" in, spoken answer out.
- **Language**: user-selectable, not hardcoded to English. Russian is the
  primary target language for STT/TTS (Whisper supports it natively; TTS
  engine choice must too).

This depends on `smarthelper chat` (§7) existing first — the web UI is a
client of that conversation loop, not a separate feature.

### 9. Non-Goals (for now)

- Multi-user / auth (by design — LAN-only, single trusted user)
- Tool sandboxing (runs as the invoking user)
- Model management (assumes Ollama/llamafile already installed)
- Internet-facing exposure of the web UI (explicitly out of scope, not just undone)

---

## Roadmap (next after this foundation)

1. `smarthelper chat`: read a user message, call `llm.Router.Chat` with the
   tool registry's definitions, execute any returned tool calls, loop until
   the model returns a final answer.
2. Real backends for at least one sensor (e.g. 1-Wire `w1_slave` for
   outdoor/fridge temperature) to replace `mock`.
3. Integration test tier (`tests/integration/`) that talks to a real Ollama
   instance when `OLLAMA_HOST` is set, skipped otherwise.
4. Local web server (LAN-only, no auth) exposing a minimal chat UI for phone
   browsers, built on top of the `chat` loop from (1).
5. Voice interface: Whisper STT + TTS, language-configurable (Russian first),
   as an alternate front-end to the same `chat` loop.
