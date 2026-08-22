// Package mcp implements a minimal Model Context Protocol server over stdio.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/roman220/bosun-smarthelper/internal/errlog"
	"github.com/roman220/bosun-smarthelper/internal/tools"
)

const protocolVersion = "2024-11-05"

// Server serves tool definitions and tool calls over the MCP stdio transport
// (newline-delimited JSON-RPC 2.0 messages).
type Server struct {
	name     string
	version  string
	registry *tools.Registry
	logger   *slog.Logger
	errLog   *errlog.Logger
}

// NewServer creates an MCP server backed by the given tool registry.
func NewServer(name, version string, registry *tools.Registry, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{name: name, version: version, registry: registry, logger: logger}
}

// SetErrorLog wires a failure log for tool call errors (see internal/errlog).
// A nil logger (the default) means failures are simply returned to the
// caller as a JSON-RPC error, not recorded anywhere durable.
func (s *Server) SetErrorLog(logger *errlog.Logger) {
	s.errLog = logger
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads JSON-RPC requests from r, one per line, and writes responses to w
// until r reaches EOF or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	enc := json.NewEncoder(w)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.logger.Warn("dropping invalid request", "error", err)
			continue
		}

		if req.ID == nil {
			// Notification (e.g. "notifications/initialized") — no response expected.
			continue
		}

		if err := enc.Encode(s.handle(ctx, req)); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, req request) response {
	resp := response{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.listTools()}
	case "tools/call":
		result, err := s.callTool(ctx, req.Params)
		if err != nil {
			resp.Error = &rpcError{Code: -32000, Message: err.Error()}
			break
		}
		resp.Result = result
	default:
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}

	return resp
}

func (s *Server) listTools() []map[string]any {
	defs := s.registry.Definitions()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		fn := d["function"].(map[string]any)
		out = append(out, map[string]any{
			"name":        fn["name"],
			"description": fn["description"],
			"inputSchema": fn["parameters"],
		})
	}
	return out
}

type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	var params callParams
	if err := json.Unmarshal(raw, &params); err != nil {
		err = fmt.Errorf("invalid tool call params: %w", err)
		s.errLog.Record("tool_call", "(unparsed)", err)
		return nil, err
	}

	tool, ok := s.registry.Get(params.Name)
	if !ok {
		err := fmt.Errorf("unknown tool: %s", params.Name)
		s.errLog.Record("tool_call", params.Name, err)
		return nil, err
	}

	result, err := tool.Execute(ctx, params.Arguments)
	if err != nil {
		err = fmt.Errorf("execute %s: %w", params.Name, err)
		s.errLog.Record("tool_call", params.Name, err)
		return nil, err
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(payload)},
		},
	}, nil
}
