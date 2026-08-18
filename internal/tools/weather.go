package tools

import (
	"context"
	"fmt"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

// WeatherTool provides outdoor weather data
type WeatherTool struct {
	config *config.WeatherConfig
}

// NewWeatherTool creates a new weather tool
func NewWeatherTool(cfg *config.WeatherConfig) *WeatherTool {
	return &WeatherTool{config: cfg}
}

func (t *WeatherTool) Name() string {
	return "get_weather"
}

func (t *WeatherTool) Description() string {
	return "Get current outdoor weather conditions (temperature, humidity)"
}

func (t *WeatherTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{
				"type":        "string",
				"description": "Location to get weather for (optional)",
			},
		},
		"additionalProperties": false,
	}
}

func (t *WeatherTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	location := "current"
	if loc, ok := args["location"].(string); ok && loc != "" {
		location = loc
	}

	// For mock type, return configured values
	if t.config.Type == "mock" {
		return map[string]any{
			"location":      location,
			"temperature_c": t.config.MockTempC,
			"humidity":      t.config.MockHumidity,
			"unit":          "celsius",
			"source":        "mock",
		}, nil
	}

	// TODO: Implement MQTT, HTTP, 1-Wire backends
	return nil, fmt.Errorf("weather sensor type %q not implemented", t.config.Type)
}
