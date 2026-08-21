# Bosun (Старпом)

Bosun — «Старпом» in the Russian interface — is a local-first AI assistant
that works offline with a local LLM (via Ollama)
and automatically prefers a remote LLM (OpenAI-compatible) when the internet
is available. Exposes sensor data through MCP (Model Context Protocol) so any
MCP-compatible client (Claude Desktop, custom agents, etc.) can query it.

## Status

In daily use, not just a prototype. Working today:

- **LLM routing** — local (Ollama) and remote (OpenAI-compatible) clients
  behind one interface, with connectivity-based selection and failover.
- **MCP server** — `smarthelper mcp` serves tools over stdio (JSON-RPC 2.0).
- **4 sensor tools** — `get_weather`, `get_fridge_temp`, `get_gps`,
  `get_system_info`; weather supports mock and live Open-Meteo backends.
- **Automatic offline tool filtering** — tools backed by internet services are
  removed from the LLM contract and web quick actions while offline.
- **One-shot chat** — `smarthelper chat` connects either LLM provider to the
  tool registry and executes tool calls until the model returns an answer.
- **LAN web UI** — `smarthelper serve` exposes the same agent loop through a
  responsive, dependency-free interface for phone and desktop browsers.
- **Streamed answers** — the web UI's final answer fills in progressively
  instead of appearing all at once (Ollama and OpenAI-compatible SSE, both
  local and remote). A tool call's raw encoding is folded into a collapsed,
  expandable detail rather than ever shown as-is — see
  `docs/streaming.md`. A **stop** button cancels an in-flight request. Only
  the local model serializes concurrent requests (it's weak, shared
  hardware) — a second request while it's busy is told its queue position
  instead of waiting silently; the remote provider handles concurrency on
  its own and is never queued.
- **Multi-turn web sessions** — prior user/assistant turns are retained by
  session ID with configurable history and expiration limits, persisted to
  disk so they survive both a page reload and a service restart.
- **Persistent local memos** — dated notes can be written, read, listed,
  archived, and deleted through the `memo` tool.
- **Semantic memo/document search** — `memo`'s `search` action finds memos
  and uploaded reference documents (manuals, how-tos) by meaning, not just
  exact words; `topics` lists what's uploaded without searching, and
  `document_id` scopes a search to one of them. Documents (plain text or
  PDF) are uploaded through the web UI only, never an LLM-callable action,
  to keep the tool contract small. A scanned/diagram page is OCR'd
  (tesseract) and carries its image alongside whatever text was
  recognized; a diagram chunk with no matching procedural text elsewhere
  in the store still surfaces on its own, and the model drops its image
  straight into the answer as markdown — see `docs/memo-search.md`.
- **Equipment maintenance tracking** — optional counter/date fields on a
  memo (`metric_name`/`metric_value`/`due_date`/`due_metric_value`) log an
  oil change, an engine-hour reading, anything with a due date; `maintenance`
  reports what's due and how much of a counter is left, computed in Go
  against the real clock, not the model. Domain-neutral by design — a car's
  odometer and a boat's main-engine hours both fit the same two fields — see
  `docs/maintenance-tracking.md`.
- **Online knowledge tools** — DuckDuckGo web search and Wikipedia summaries,
  automatically hidden while offline.
- **Settings page** — the web UI's gear icon lets you edit the assistant's
  name/style prompt, default language, LLM temperatures, and the memo tag
  auto-normalization vocabulary live, no restart needed — see
  `docs/settings.md`.
- **HTTPS via mkcert** — a LAN IP has no public CA to issue it a cert, so
  the web UI can serve TLS using an mkcert-issued cert/key instead, trusted
  with no browser warning once the CA is installed on a device — see
  `docs/tls.md`.
- **Failure log** — tool and LLM-call errors are recorded to one file
  (`internal/errlog`), reviewable with `smarthelper errors`, to drive an
  improvement loop instead of disappearing into stderr.
- **Voice, both directions** — a 🔊 button speaks any assistant reply out
  loud (Piper), and a press-and-hold 🎤 button transcribes speech
  (whisper.cpp) into the same chat flow a typed message uses, auto-speaking
  the reply back — fully offline, no Python anywhere in the path — see
  `docs/voice.md`.
- **Remote access via Cloudflare Tunnel** — an outbound-only `cloudflared`
  connector exposes a dedicated domain without forwarding any router
  ports, additive to (not a replacement for) direct LAN access — see
  `docs/cloudflare.md`.
- **Local monitoring dashboard** — a 📊 button charts CPU/memory/disk/GPS/
  fridge over time from a local SQLite store, a personal bounded-history
  analog to MRTG/Grafana; which sensors it samples is a `config.yaml` list,
  not hardcoded, so a future sensor (battery, water tank) is a config edit
  once its tool exists — see `docs/monitoring.md`.

Not yet built: real fridge sensor hardware, continuous voice conversation
mode. See `SPEC.md` for the full roadmap, and
[`docs/README.md`](docs/README.md) for a topic-organized index of every
doc referenced throughout this file.

## Architecture

```
User Query
    │
    ▼
┌─────────────────┐
│  LLM Router     │ ──online, remote configured──▶ Remote LLM (OpenAI-compatible)
│  (connectivity) │                                        │ fails
└────────┬────────┘                                        ▼
         │ offline / no remote                    Local LLM (Ollama)
         ▼
┌─────────────────┐
│  Local LLM      │ ──▶ Ollama (llama3.1, qwen2.5, etc.)
│  (Ollama)       │
└─────────────────┘

         LLM response with tool calls
                    │
                    ▼
          ┌─────────────────┐
          │  Agent loop     │ ──▶ execute tool ──▶ send result back to LLM
          └─────────────────┘

┌─────────────────┐
│  MCP Server     │ ──▶ get_weather, get_fridge_temp, get_gps, get_system_info
│  (stdio)        │
└─────────────────┘
```

## Quick Start

### Prerequisites

- Go 1.23+
- Ollama for local LLM: `curl -fsSL https://ollama.ai/install.sh | sh`, then
  `ollama pull llama3.1:8b`

### Build

```bash
make build          # -> bin/smarthelper
# or
go build -o bin/smarthelper ./cmd/smarthelper
```

### Configure

```bash
mkdir -p ~/.config/smarthelper
cp configs/config.yaml.example ~/.config/smarthelper/config.yaml
cp .env.example .env   # fill in OPENAI_API_KEY if you want remote mode
```

`config.yaml` defaults to mock sensor values and local Ollama — it works with
zero edits for local-only testing.

### Run

```bash
# Start the MCP server (stdio transport) — for Claude Desktop or any MCP client
./bin/smarthelper mcp

# Ask one question through the selected LLM, with tool access
./bin/smarthelper chat "What's the weather?"

# Run the browser interface on the configured private address
./bin/smarthelper serve

# Review recent tool/LLM failures (see internal/errlog)
./bin/smarthelper errors

# Print version
./bin/smarthelper version
```

Point an MCP client at `./bin/smarthelper mcp` as a stdio server to get access
to the tools below.

The web interface defaults to `127.0.0.1:8080`. For phone access, configure an
explicit private address such as `10.0.0.111:8080`, or `0.0.0.0:8080` if the
host's IP isn't fixed (see `docs/tls.md`); public binds are rejected. No
authentication is provided, so keep it on a trusted LAN.

## MCP Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `get_weather` | Current conditions and up to 16 forecast days | `location?`, `forecast_days?` (1–16) |
| `get_fridge_temp` | Refrigerator/freezer temperature | `zone?` (`fridge`\|`freezer`) |
| `get_gps` | Coordinates, speed, altitude | — |
| `get_system_info` | CPU (incl. temperature), RAM, disk, uptime | `include?` (`cpu`,`memory`,`disk`,`host`) |
| `memo` | Persistent dated notes; `search` finds memos and uploaded documents by meaning; `topics` lists uploaded documents without searching; `tag`/`document_id` narrow either; `maintenance` reports due equipment upkeep logged via the metric/due-date fields | `action`, `key?`, `content?`, `tags?`, `include_archived?`, `tag?`, `query?`, `limit?`, `document_id?`, `metric_name?`, `metric_value?`, `due_date?`, `due_metric_value?` |
| `web_search` | DuckDuckGo web results | `query`, `limit?` |
| `wikipedia` | Wikipedia summary and source URL | `title`, `lang?` |
| `get_directions` | Google/Apple Maps links for a destination (route from the current GPS location when available) | `destination` |

All four sensor tools have local/mock paths. Weather can use `type:
open_meteo` for live weather without an API key; it resolves cities through
Open-Meteo, named landmarks through Nominatim when needed, and fetches
forecasts from Open-Meteo. This backend is automatically hidden from the
model while offline. The mock GPS defaults to Salt Lake City (`40.7608,
-111.8910`).

`get_directions` uses the same geocoding cascade (Open-Meteo, then Nominatim
for named landmarks) to resolve a destination, then returns map links rather
than guessing when the place is ambiguous or not found — the model is
instructed to ask for a more specific place instead.

See [`docs/offline-mode.md`](docs/offline-mode.md) for connectivity behavior
and the contract required by future online tools, and
[`docs/token-budget.md`](docs/token-budget.md) for how the tool contract,
system prompt, and chat history are kept small enough for a weak local model.

## Chat sessions and memos

The web client keeps a random session ID in browser local storage and, on
load, fetches `/api/history?session_id=...` to repopulate the visible
transcript — so a page reload doesn't wipe the conversation you can see.
Bosun keeps completed user/assistant turns (tool protocol messages are not
retained) and supplies them to the next model request; how much of that
history is actually sent depends on which provider is serving the request —
see `docs/token-budget.md`. “Clear chat” deletes the server-side session and
creates a new ID.

Sessions are persisted to disk (`web.session_store_path`, default
`~/.local/share/bosun/sessions.json`), atomically like the memo store, so a
service restart doesn't lose chat history either — only an expired TTL or an
explicit “clear chat” does. History limits, TTL, and maximum session count
are configured under `web`.

Memos are separate from transient chat history. They are stored atomically in
`~/.local/share/bosun/memos.json` by default and survive service restarts. Each
memo exposes `created_at`, `updated_at`, `status`, and computed `age_days`.
Archived notes also have `archived_at`. `list` lets the model review old notes;
`archive` keeps a note out of the active list, while `delete` physically
removes it.

## Docker

```bash
make docker-up
```

Runs the Bosun app plus two `llama-server` containers (`llama-chat` for the
local model, `llama-embed` for memo semantic search) — see `docs/docker.md`
for why they're built from source, plus networking, volumes, and one-off
command usage.

## Development

```bash
make check   # gofmt -l, go vet, go test, go build — same as CI
make test    # just the tests
make lint    # fmt + vet only
```

Machine-specific systemd service details and commands are documented in
[`docs/local-deployment.md`](docs/local-deployment.md).

## Configuration Reference

Full annotated example: `configs/config.yaml.example`. Env vars override the
file with an `SMARTHELPER_` prefix and underscores for nesting, e.g.
`SMARTHELPER_LLM_LOCAL_MODEL=qwen2.5:3b`. The actual LLM API key is read
directly from the env var named by `llm.remote.api_key_env` (default
`OPENAI_API_KEY`) — it is never routed through the `SMARTHELPER_` prefix.

The local provider supports both native Ollama (`api_format: ollama`) and
OpenAI-compatible servers such as LM Studio (`api_format: openai`). For the
latter, set a `/v1` base URL and optionally name a key environment variable in
`llm.local.api_key_env`.

### Why the local llama.cpp setup uses XML tool calls

The Qwen GGUF model on the current host has a tool-aware chat template, but
its emitted calls are not reliably converted by llama.cpp into OpenAI
`tool_calls`. The service therefore starts `llama-server` with
`--skip-chat-parsing`: llama.cpp returns the model's raw template output, such
as `<tool_call><function=get_weather>...`, and Bosun converts that XML
into the same internal `ToolCall` structure used by remote OpenAI-compatible
models.

This is not XML for the application API and it is never shown deliberately to
the user. It is a compatibility adapter that lets the small local model use
the normal agent/tool loop instead of merely describing a tool call in prose.
If a local server cannot produce even the template XML, set
`supports_tools: false`; Bosun then uses a stricter JSON-in-prompt
fallback. Tool definitions are sorted before requests so repeated prompts stay
stable and llama.cpp can reuse its prompt/KV cache more effectively.

Machine-specific flags and service commands are documented in
[`docs/local-deployment.md`](docs/local-deployment.md).

Transient remote failures (network errors, HTTP 429, and HTTP 5xx) are retried
with configurable exponential backoff before the router falls back to the
local model.

Connectivity also controls tool exposure. When the check target is
unreachable, the local LLM receives only tools whose configured backends work
offline. The server rejects a stale online-tool call if connectivity changes
mid-request, and `/api/status` tells the browser which quick actions remain
available.

## Contributing

Read `AGENTS.md` first — it covers code style, testing expectations, and how
to add a new sensor tool.

## Credits

- Ship's bell chime (web UI): "Ship Bell" by Sojan, CC0 / public domain —
  https://freesound.org/s/353232/
- Candidate source for future ambient background audio (ocean wind, ship
  creaking) — royalty-free per the video's own listing:
  [Pirate Ship Ambience Sound Effects / Ocean Wind and Ship Creaking Sleeping Sounds](https://www.youtube.com/watch?v=dr9aAyuYjSk&list=PLCnngi2mdPv0TF_7ucFQbhMxKW7wUMvTr)

## License

MIT — see `LICENSE`.
