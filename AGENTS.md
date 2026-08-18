# Agent Rules for ai-local-smarthelper

## General Principles

- **All documentation in English** — code comments, README, config examples, commit messages
- **Conventional Commits** — `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`
- **No hardcoded secrets** — API keys, tokens, passwords only via environment variables
- **Pure functions for MCP tools** — input → output, no side effects, easy to test
- **Fail fast** — validate config at startup, exit with clear error messages

## Code Style

- Go: standard `gofmt`, `golangci-lint`
- Package names: short, lowercase, no underscores
- Interfaces in parent package, implementations in subpackages
- Errors: wrap with `fmt.Errorf("context: %w", err)`

## Testing

- Unit tests for all public functions in `*_test.go`
- Integration tests in `tests/integration/` — require external deps (Ollama, MQTT)
- Mock external services in unit tests
- Run `go test ./...` before commit

## Architecture

```
cmd/smarthelper/          # Entry point
internal/
  agent/                  # Main agent loop (future)
  llm/                    # LLM provider abstraction
    local.go              # Ollama client
    remote.go             # OpenAI-compatible client
    router.go             # Auto-switch: online→remote, offline→local
  mcp/                    # MCP server/client
    server.go             # STDIO transport, tool registry
    client.go             # Calls external MCP servers (future)
    tools/                # Built-in sensor tools
      weather.go
      fridge.go
      gps.go
      system.go
  config/                 # Config loading (viper + yaml)
  health/                 # Health checks, connectivity
configs/
  config.yaml.example
```

## LLM Router Logic

1. Check internet connectivity (ping 8.8.8.8 or HTTP HEAD to api.openai.com)
2. If online + remote configured → use remote (OpenAI-compatible)
3. If offline or remote unavailable → fallback to local (Ollama)
4. Log which provider is used for each request

## MCP Tools Contract

Each tool implements:
```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() jsonschema.Schema
    Execute(ctx context.Context, args map[string]any) (any, error)
}
```

Tools return structured data (not strings) — LLM formats the response.

## Config Management

- `viper` for config loading (YAML + env vars)
- Env vars override config file: `LLM_REMOTE_API_KEY`, `LLM_LOCAL_MODEL`, etc.
- Example config in `configs/config.yaml.example`
- Real config at `~/.config/smarthelper/config.yaml` or `./config.yaml`

## Git Workflow

- `main` branch protected
- Feature branches: `feat/description`, `fix/description`
- PR required for merge
- CI: `go build`, `go test`, `golangci-lint`

## Adding a New Sensor Tool

1. Create `internal/tools/<name>.go` implementing `Tool` interface
2. Register in `internal/mcp/server.go` tool registry
3. Add config section in `configs/config.yaml.example`
4. Write unit test in `internal/tools/<name>_test.go`
5. Update README with tool description
