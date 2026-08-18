# Bosun (Старпом)

Bosun — «Старпом» in the Russian interface — is a local-first AI assistant
that works offline with a local LLM (via Ollama)
and automatically prefers a remote LLM (OpenAI-compatible) when the internet
is available. Exposes sensor data through MCP (Model Context Protocol) so any
MCP-compatible client (Claude Desktop, custom agents, etc.) can query it.

## Status

Early foundation. Working today:

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
- **Multi-turn web sessions** — prior user/assistant turns are retained by
  session ID with configurable history and expiration limits.
- **Persistent local memos** — dated notes can be written, read, listed,
  archived, and deleted through the `memo` tool.
- **Online knowledge tools** — DuckDuckGo web search and Wikipedia summaries,
  automatically hidden while offline.

Not yet built: chat persistence across service restarts and real sensor
hardware backends.
See `SPEC.md` for the full roadmap.

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

# Print version
./bin/smarthelper version
```

Point an MCP client at `./bin/smarthelper mcp` as a stdio server to get access
to the tools below.

The web interface defaults to `127.0.0.1:8080`. For phone access, configure an
explicit private address such as `10.0.0.111:8080`; wildcard and public binds
are rejected. No authentication is provided, so keep it on a trusted LAN.

## MCP Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `get_weather` | Current conditions and up to 16 forecast days | `location?`, `forecast_days?` (1–16) |
| `get_fridge_temp` | Refrigerator/freezer temperature | `zone?` (`fridge`\|`freezer`) |
| `get_gps` | Coordinates, speed, altitude | — |
| `get_system_info` | CPU, RAM, disk, uptime | `include?` (`cpu`,`memory`,`disk`,`host`) |
| `memo` | Persistent dated notes | `action`, `key?`, `content?`, `include_archived?` |
| `web_search` | DuckDuckGo web results | `query`, `limit?` |
| `wikipedia` | Wikipedia summary and source URL | `title`, `lang?` |

All four have local/mock paths. Weather can use `type: open_meteo` for live
weather without an API key; it resolves cities through Open-Meteo, named
landmarks through Nominatim when needed, and fetches forecasts from
Open-Meteo. This backend is automatically hidden from the model while
offline. The mock GPS defaults to Salt Lake City (`40.7608, -111.8910`).

See [`docs/offline-mode.md`](docs/offline-mode.md) for connectivity behavior
and the contract required by future online tools, and
[`docs/token-budget.md`](docs/token-budget.md) for how the tool contract,
system prompt, and chat history are kept small enough for a weak local model.

## Chat sessions and memos

The web client keeps a random session ID in browser local storage. Bosun keeps
completed user/assistant turns in memory and supplies them to the next model
request. Tool protocol messages are not retained. “Clear chat” deletes the
server-side session and creates a new ID. History limits, TTL, and maximum
session count are configured under `web`; service restart clears chat history.

Memos are separate from transient chat history. They are stored atomically in
`~/.local/share/bosun/memos.json` by default and survive service restarts. Each
memo exposes `created_at`, `updated_at`, `status`, and computed `age_days`.
Archived notes also have `archived_at`. `list` lets the model review old notes;
`archive` keeps a note out of the active list, while `delete` physically
removes it.

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

## License

MIT — see `LICENSE`.
