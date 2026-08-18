# ai-local-smarthelper

A local-first AI assistant that works offline with a local LLM (via Ollama)
and automatically prefers a remote LLM (OpenAI-compatible) when the internet
is available. Exposes sensor data through MCP (Model Context Protocol) so any
MCP-compatible client (Claude Desktop, custom agents, etc.) can query it.

## Status

Early foundation. Working today:

- **LLM routing** — local (Ollama) and remote (OpenAI-compatible) clients
  behind one interface, with connectivity-based selection and failover.
- **MCP server** — `smarthelper mcp` serves tools over stdio (JSON-RPC 2.0).
- **4 sensor tools** — `get_weather`, `get_fridge_temp`, `get_gps`,
  `get_system_info`, currently backed by mock/configurable values.

Not yet built: a conversation loop that connects the LLM to the tools
(`smarthelper chat`), and real sensor hardware backends. See `SPEC.md` for the
full roadmap.

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

# Print version
./bin/smarthelper version
```

Point an MCP client at `./bin/smarthelper mcp` as a stdio server to get access
to the tools below.

## MCP Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `get_weather` | Outdoor temperature, humidity | `location?` |
| `get_fridge_temp` | Refrigerator/freezer temperature | `zone?` (`fridge`\|`freezer`) |
| `get_gps` | Coordinates, speed, altitude | — |
| `get_system_info` | CPU, RAM, disk, uptime | `include?` (`cpu`,`memory`,`disk`,`host`) |

All four ship with `type: mock` in the example config — swap to a real
backend (1-Wire, MQTT, serial GPS) per sensor as it's implemented.

## Development

```bash
make check   # gofmt -l, go vet, go test, go build — same as CI
make test    # just the tests
make lint    # fmt + vet only
```

## Configuration Reference

Full annotated example: `configs/config.yaml.example`. Env vars override the
file with an `SMARTHELPER_` prefix and underscores for nesting, e.g.
`SMARTHELPER_LLM_LOCAL_MODEL=qwen2.5:3b`. The actual LLM API key is read
directly from the env var named by `llm.remote.api_key_env` (default
`OPENAI_API_KEY`) — it is never routed through the `SMARTHELPER_` prefix.

## Contributing

Read `AGENTS.md` first — it covers code style, testing expectations, and how
to add a new sensor tool.

## License

MIT — see `LICENSE`.
