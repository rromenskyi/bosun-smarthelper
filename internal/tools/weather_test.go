package tools

import (
	"context"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestWeatherTool_Mock(t *testing.T) {
	cfg := &config.WeatherConfig{Type: "mock", MockTempC: 21.5, MockHumidity: 55}
	tool := NewWeatherTool(cfg)

	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	data, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", result)
	}
	if data["temperature_c"] != 21.5 {
		t.Errorf("temperature_c = %v, want 21.5", data["temperature_c"])
	}
}

func TestWeatherTool_UnsupportedBackend(t *testing.T) {
	tool := NewWeatherTool(&config.WeatherConfig{Type: "mqtt"})

	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected error for unimplemented backend, got nil")
	}
}
