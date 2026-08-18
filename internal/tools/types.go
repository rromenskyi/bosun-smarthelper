package tools

import (
	"context"
	"sort"
)

// Tool defines the interface for MCP tools
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any // JSON Schema
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// NetworkDependentTool marks tools whose configured backend requires internet
// access. The agent omits these tools from the model contract while offline.
type NetworkDependentTool interface {
	RequiresNetwork() bool
}

// Registry holds all available tools
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a new tool registry
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get returns a tool by name
func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tool names
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AvailableList returns tools usable with the current connectivity state.
func (r *Registry) AvailableList(online bool) []string {
	names := make([]string, 0, len(r.tools))
	for name, tool := range r.tools {
		if requiresNetwork(tool) && !online {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsAvailable reports whether a tool can run with the current connectivity.
func (r *Registry) IsAvailable(name string, online bool) bool {
	tool, ok := r.tools[name]
	return ok && (online || !requiresNetwork(tool))
}

func requiresNetwork(tool Tool) bool {
	networkTool, ok := tool.(NetworkDependentTool)
	return ok && networkTool.RequiresNetwork()
}

// Definitions returns tool definitions for LLM function calling
func (r *Registry) Definitions() []map[string]any {
	defs := make([]map[string]any, 0, len(r.tools))
	for _, name := range r.List() {
		tool := r.tools[name]
		defs = append(defs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name(),
				"description": tool.Description(),
				"parameters":  tool.InputSchema(),
			},
		})
	}
	return defs
}
