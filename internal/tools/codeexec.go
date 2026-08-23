package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

// sessionIDContextKey is unexported so only this package's own
// ContextWithSessionID/SessionIDFromContext can set or read it — the same
// pattern Go's own context package documents for avoiding key collisions
// across packages.
type sessionIDContextKey struct{}

// DefaultCodeExecSessionID is the workspace CodeExecTool uses when ctx
// carries no real session id — the CLI (`smarthelper chat`) and MCP paths
// have no chat-session concept at all, so every non-web invocation shares
// this one workspace. Fine for a single-owner personal appliance; the web
// UI gets real per-browser-session isolation via ContextWithSessionID
// (see internal/webui/server.go) instead.
const DefaultCodeExecSessionID = "default-session"

// ContextWithSessionID attaches a chat session id to ctx so CodeExecTool
// can scope a run_code workspace to the conversation it came from, without
// the LLM ever supplying (or being able to forge) that id itself — see
// docs/sandbox.md for why session_id was deliberately kept out of the
// tool's own InputSchema.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDContextKey{}, sessionID)
}

// SessionIDFromContext reads back what ContextWithSessionID set.
func SessionIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(sessionIDContextKey{}).(string)
	return id, ok && id != ""
}

// CodeExecTool lets the LLM write and run a short Python program — for
// math, parsing, or simulation it would otherwise reason about (badly)
// itself. The code never runs inside the bosun process or container: this
// is just an HTTP client to a separate sandboxd service that owns the
// actual Docker-container isolation (internal/sandbox, docs/sandbox.md).
// If sandboxd isn't reachable, Execute returns a plain error the agent
// loop surfaces to the model — never a crash — since this tool must stay
// entirely optional (config.yaml's sandbox.enabled, off by default).
type CodeExecTool struct {
	baseURL string
	client  *http.Client
}

// NewCodeExecTool builds the tool from config.yaml's sandbox section.
func NewCodeExecTool(cfg *config.SandboxConfig) *CodeExecTool {
	timeout := time.Duration(cfg.TimeoutSeconds+10) * time.Second
	return &CodeExecTool{
		baseURL: strings.TrimRight(cfg.URL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

func (t *CodeExecTool) Name() string { return "run_code" }

func (t *CodeExecTool) Description() string {
	return "Run a short Python 3 program and get back its stdout/stderr/exit code — use for math, parsing, or " +
		"simulation, not things the built-in tools already answer directly. Standard library only unless you " +
		"install a package yourself; full network access; files written to the workspace persist across calls " +
		"within this same conversation, but nothing else does."
}

func (t *CodeExecTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code": map[string]any{"type": "string", "description": "Python 3 source to run."},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     120,
				"description": "Wall-clock limit in seconds; omit to use the server's default.",
			},
		},
		"required":             []string{"code"},
		"additionalProperties": false,
	}
}

// codeExecRequest/codeExecResponse mirror internal/sandbox.Server's own
// request/response shapes exactly — this is the one HTTP contract between
// the two, kept in sync by hand since they're deliberately separate
// packages/binaries (see docs/sandbox.md for why).
type codeExecRequest struct {
	SessionID      string `json:"session_id"`
	Code           string `json:"code"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type codeExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Error    string `json:"error,omitempty"`
}

func (t *CodeExecTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	code, _ := args["code"].(string)
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("code is required")
	}
	timeoutSeconds, err := integerArgument(args["timeout_seconds"], 0, 1, 120)
	if err != nil {
		return nil, fmt.Errorf("timeout_seconds: %w", err)
	}

	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		sessionID = DefaultCodeExecSessionID
	}

	payload, err := json.Marshal(codeExecRequest{SessionID: sessionID, Code: code, TimeoutSeconds: timeoutSeconds})
	if err != nil {
		return nil, fmt.Errorf("encode sandbox request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/run", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create sandbox request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("code execution sandbox is not reachable — is sandboxd enabled and running? (%w)", err)
	}
	defer resp.Body.Close()

	var result codeExecResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode sandbox response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if result.Error != "" {
			return nil, fmt.Errorf("code execution sandbox: %s", result.Error)
		}
		return nil, fmt.Errorf("code execution sandbox returned HTTP %d", resp.StatusCode)
	}

	return map[string]any{
		"stdout":    result.Stdout,
		"stderr":    result.Stderr,
		"exit_code": result.ExitCode,
		"timed_out": result.TimedOut,
	}, nil
}
