package tools

import (
	"context"
	"fmt"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

// GPSTool provides GPS location and movement data
type GPSTool struct {
	config *config.GPSConfig
}

// NewGPSTool creates a new GPS tool
func NewGPSTool(cfg *config.GPSConfig) *GPSTool {
	return &GPSTool{config: cfg}
}

func (t *GPSTool) Name() string {
	return "get_gps"
}

func (t *GPSTool) Description() string {
	return "Get current GPS coordinates, speed, and altitude"
}

func (t *GPSTool) InputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (t *GPSTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if t.config.Type == "mock" {
		return map[string]any{
			"latitude":   t.config.MockLatitude,
			"longitude":  t.config.MockLongitude,
			"speed_kmh":  t.config.MockSpeedKMH,
			"altitude_m": t.config.MockAltitudeM,
			"source":     "mock",
		}, nil
	}

	// TODO: Implement serial/NMEA backend
	return nil, fmt.Errorf("gps sensor type %q not implemented", t.config.Type)
}
