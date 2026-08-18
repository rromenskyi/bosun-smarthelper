# Agent Rules for ai-local-smarthelper

Instructions for any AI agent (or human) working in this repository.

## General Principles

- **English only** — code, comments, docs, commit messages, PR descriptions.
- **Conventional Commits** — `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`.
- **No hardcoded secrets** — API keys, tokens, credentials only via environment
  variables (see `.env.example`). Never commit `.env` or `config.yaml`.
- **Local-first** — every user-facing feature must have a working offline path
  (local LLM + mock/local sensor backends). Remote/online is an enhancement,
  never a hard dependency.
- **MCP-first** — new sensor/actuator capabilities are added as MCP tools
  (`internal/tools/`), not hardcoded into the LLM prompt or router logic.
- **Config over code** — endpoints, model names, timeouts, sensor backends are
  config-driven (`configs/config.yaml.example` + env overrides), not literals
  buried in Go code.
- **Small, focused commits.** No unrelated refactors bundled into a feature commit.
- **Don't invent working features.** If something isn't implemented yet, say so
  in the README/SPEC roadmap instead of stubbing it out silently.

## Code Style

- Go: standard `gofmt`; keep it clean (`gofmt -l .` must report nothing).
- Package names: short, lowercase, no underscores.
- Interfaces live in the package that consumes them; concrete implementations
  in their own files (e.g. `llm.Client` interface in `types.go`, implemented by
  `LocalClient`/`RemoteClient`).
- Errors: wrap with `fmt.Errorf("context: %w", err)`.

## Testing

- Unit tests for all public functions/tools, in `*_test.go` next to the code.
- Mock sensor backends (`type: mock` in config) make tools trivially testable
  without real hardware.
- Run `make check` (fmt + vet + test + build) before every commit — this is
  the same thing CI runs.

## Architecture

```
cmd/smarthelper/            # CLI entry point (cobra): version, mcp
internal/
  llm/                      # LLM provider abstraction
    types.go                # Message, Client interface, ToolDefinition
    local.go                # Ollama client (offline)
    remote.go                # OpenAI-compatible client (online)
    router.go                # Connectivity check + local/remote selection
  mcp/                      # MCP server (stdio, JSON-RPC 2.0)
    server.go                # initialize / tools/list / tools/call
  tools/                    # Built-in MCP tools (sensors)
    types.go                # Tool interface + Registry
    weather.go, fridge.go, gps.go, system.go
  config/                   # Config loading (viper: YAML + env vars)
  webui/                    # Embedded LAN-only browser UI + JSON chat API
configs/
  config.yaml.example
```

`internal/llm` (routing between local/remote models) and `internal/mcp`
(exposing tools) remain independent building blocks. `internal/agent` connects
an LLM conversation directly to the same tool registry for the one-shot
`smarthelper chat` command and the `smarthelper serve` web interface.

## LLM Router Logic (`internal/llm/router.go`)

1. Periodically check connectivity via HTTP HEAD to `llm.router.check_target`.
2. If online and a remote client is configured → prefer remote (OpenAI-compatible).
3. If offline, remote unconfigured, or the remote call fails → fall back to local (Ollama).
4. Callers should log which provider actually served each request.

## MCP Tools Contract

Each tool implements `tools.Tool` (`internal/tools/types.go`):

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() map[string]any // JSON Schema
    Execute(ctx context.Context, args map[string]any) (any, error)
}
```

Tools return structured data (maps/structs), not preformatted strings — the
LLM (or MCP client) is responsible for presenting it to the user.

## Config Management

- `viper` loads YAML + environment variables.
- Search order: `./config.yaml`, `~/.config/smarthelper/config.yaml`, `/etc/smarthelper/config.yaml`.
- Env vars override the file, prefixed `SMARTHELPER_` with dots replaced by
  underscores, e.g. `SMARTHELPER_LLM_LOCAL_MODEL`, `SMARTHELPER_LLM_ROUTER_PREFER_REMOTE`.
- The actual remote API key is **not** read through viper/the prefix — it's
  read directly from whatever env var `llm.remote.api_key_env` names (default
  `OPENAI_API_KEY`), so secrets never need an `SMARTHELPER_`-prefixed alias.
- Example config: `configs/config.yaml.example`.

## Git Workflow

- `main` is the default branch.
- Feature branches: `feat/description`, `fix/description`.
- CI equivalent to run locally before pushing: `make check`.

## Adding a New Sensor Tool

1. Create `internal/tools/<name>.go` implementing the `Tool` interface.
2. Register it in `cmd/smarthelper/main.go` (`registry.Register(...)`).
3. Add a config struct + defaults in `internal/config/config.go` and document
   it in `configs/config.yaml.example`.
4. Write `internal/tools/<name>_test.go` covering at least the mock backend.
5. Update the tools table in `README.md`.
