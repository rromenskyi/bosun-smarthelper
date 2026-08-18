package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/errlog"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

func newTestServer() *Server {
	registry := tools.NewRegistry()
	registry.Register(tools.NewWeatherTool(&config.WeatherConfig{Type: "mock", MockTempC: 20, MockHumidity: 50}))
	return NewServer("smarthelper-test", "0.0.0-test", registry, nil)
}

func TestServer_ToolsListAndCall(t *testing.T) {
	server := newTestServer()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_weather","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"unknown/method"}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	decoder := json.NewDecoder(&out)

	var initResp response
	if err := decoder.Decode(&initResp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize failed: %+v", initResp.Error)
	}

	var listResp response
	if err := decoder.Decode(&listResp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	result := listResp.Result.(map[string]any)
	toolList := result["tools"].([]any)
	if len(toolList) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(toolList))
	}

	var callResp response
	if err := decoder.Decode(&callResp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	if callResp.Error != nil {
		t.Fatalf("tools/call failed: %+v", callResp.Error)
	}

	var unknownResp response
	if err := decoder.Decode(&unknownResp); err != nil {
		t.Fatalf("decode unknown method response: %v", err)
	}
	if unknownResp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestServer_RecordsFailedToolCallToErrorLog(t *testing.T) {
	server := newTestServer()
	logPath := filepath.Join(t.TempDir(), "errors.jsonl")
	errLog, err := errlog.Open(logPath)
	if err != nil {
		t.Fatalf("errlog.Open: %v", err)
	}
	server.SetErrorLog(errLog)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}` + "\n"
	var out bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	errLog.Close()

	entries, err := errlog.ReadAll(logPath)
	if err != nil {
		t.Fatalf("errlog.ReadAll: %v", err)
	}
	if len(entries) != 1 || entries[0].Category != "tool_call" || entries[0].Detail != "does_not_exist" {
		t.Fatalf("entries = %#v, want one tool_call/does_not_exist entry", entries)
	}
}
