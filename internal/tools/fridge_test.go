package tools

import (
	"context"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestFridgeTool_Mock(t *testing.T) {
	cfg := &config.FridgeConfig{Type: "mock", MockFridgeC: 4.0, MockFreezerC: -18.0}
	tool := NewFridgeTool(cfg)

	result, err := tool.Execute(context.Background(), map[string]any{"zone": "fridge"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	data := result.(map[string]any)
	if _, hasFreezer := data["freezer_c"]; hasFreezer {
		t.Error("expected freezer_c to be omitted when zone=fridge")
	}
	if data["fridge_c"] != 4.0 {
		t.Errorf("fridge_c = %v, want 4.0", data["fridge_c"])
	}
}
