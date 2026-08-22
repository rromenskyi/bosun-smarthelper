package tools

import (
	"context"
	"fmt"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

// FridgeTool provides refrigerator temperature data
type FridgeTool struct {
	config *config.FridgeConfig
}

// NewFridgeTool creates a new fridge tool
func NewFridgeTool(cfg *config.FridgeConfig) *FridgeTool {
	return &FridgeTool{config: cfg}
}

func (t *FridgeTool) Name() string {
	return "get_fridge_temp"
}

func (t *FridgeTool) Description() string {
	return "Get current refrigerator and freezer temperatures"
}

func (t *FridgeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"zone": map[string]any{
				"type":        "string",
				"description": "Zone to query: 'fridge' or 'freezer' (optional, returns both if omitted)",
				"enum":        []string{"fridge", "freezer"},
			},
		},
		"additionalProperties": false,
	}
}

func (t *FridgeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	zone := ""
	if z, ok := args["zone"].(string); ok {
		zone = z
	}

	if t.config.Type == "mock" {
		result := map[string]any{
			"source": "mock",
		}

		if zone == "" || zone == "fridge" {
			result["fridge_c"] = t.config.MockFridgeC
		}
		if zone == "" || zone == "freezer" {
			result["freezer_c"] = t.config.MockFreezerC
		}
		return result, nil
	}

	return nil, fmt.Errorf("fridge sensor type %q not implemented", t.config.Type)
}
