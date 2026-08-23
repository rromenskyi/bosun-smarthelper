package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

func TestCodeExecToolSendsSessionIDFromContextNotArgs(t *testing.T) {
	var gotRequest codeExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotRequest)
		json.NewEncoder(w).Encode(codeExecResponse{Stdout: "4\n", ExitCode: 0})
	}))
	defer server.Close()

	tool := NewCodeExecTool(&config.SandboxConfig{URL: server.URL, TimeoutSeconds: 30})
	ctx := ContextWithSessionID(context.Background(), "abc123session")
	// session_id in args must never override the real one from ctx — the
	// LLM can't forge or choose it (see docs/sandbox.md).
	result, err := tool.Execute(ctx, map[string]any{"code": "print(2+2)", "session_id": "forged"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotRequest.SessionID != "abc123session" {
		t.Errorf("session_id sent = %q, want the ctx value, not the args one", gotRequest.SessionID)
	}
	if gotRequest.Code != "print(2+2)" {
		t.Errorf("code sent = %q", gotRequest.Code)
	}
	resultMap, ok := result.(map[string]any)
	if !ok || resultMap["stdout"] != "4\n" {
		t.Errorf("result = %+v, want stdout 4\\n", result)
	}
}

func TestCodeExecToolFallsBackToDefaultSessionWithNoContextValue(t *testing.T) {
	var gotRequest codeExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotRequest)
		json.NewEncoder(w).Encode(codeExecResponse{})
	}))
	defer server.Close()

	tool := NewCodeExecTool(&config.SandboxConfig{URL: server.URL, TimeoutSeconds: 30})
	if _, err := tool.Execute(context.Background(), map[string]any{"code": "pass"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotRequest.SessionID != DefaultCodeExecSessionID {
		t.Errorf("session_id sent = %q, want %q (CLI/MCP fallback)", gotRequest.SessionID, DefaultCodeExecSessionID)
	}
}

func TestCodeExecToolRequiresCode(t *testing.T) {
	tool := NewCodeExecTool(&config.SandboxConfig{URL: "http://127.0.0.1:0", TimeoutSeconds: 30})
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected an error for missing code")
	}
}

func TestCodeExecToolSurfacesSandboxError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(codeExecResponse{Error: "invalid session_id"})
	}))
	defer server.Close()

	tool := NewCodeExecTool(&config.SandboxConfig{URL: server.URL, TimeoutSeconds: 30})
	_, err := tool.Execute(context.Background(), map[string]any{"code": "pass"})
	if err == nil || !strings.Contains(err.Error(), "invalid session_id") {
		t.Errorf("err = %v, want it to mention the sandbox's own error message", err)
	}
}

func TestCodeExecToolReturnsClearErrorWhenSandboxIsUnreachable(t *testing.T) {
	// A closed listener: connections fail immediately instead of hanging.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	tool := NewCodeExecTool(&config.SandboxConfig{URL: "http://" + addr, TimeoutSeconds: 30})
	_, err = tool.Execute(context.Background(), map[string]any{"code": "pass"})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err = %v, want a clear \"not reachable\" message, not a raw connection error", err)
	}
}
