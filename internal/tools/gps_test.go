package tools

import (
	"context"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

func TestGPSTool_Mock(t *testing.T) {
	cfg := &config.GPSConfig{Type: "mock", MockLatitude: 55.7558, MockLongitude: 37.6173, MockSpeedKMH: 42}
	tool := NewGPSTool(cfg)

	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	data := result.(map[string]any)
	if data["speed_kmh"] != 42.0 {
		t.Errorf("speed_kmh = %v, want 42.0", data["speed_kmh"])
	}
}
