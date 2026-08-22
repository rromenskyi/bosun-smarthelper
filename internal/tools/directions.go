package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

// DirectionsTool resolves a destination to coordinates and returns Google
// Maps and Apple Maps links — a route from the current GPS location when
// available, otherwise a direct point link.
type DirectionsTool struct {
	config *config.MapsConfig
	gps    *config.GPSConfig
	client *http.Client
}

// NewDirectionsTool creates a directions/maps-link tool.
func NewDirectionsTool(cfg *config.MapsConfig, gpsCfg *config.GPSConfig) *DirectionsTool {
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &DirectionsTool{config: cfg, gps: gpsCfg, client: &http.Client{Timeout: timeout}}
}

func (t *DirectionsTool) Name() string { return "get_directions" }

// RequiresNetwork reports that this tool needs a public geocoding service.
func (t *DirectionsTool) RequiresNetwork() bool { return true }

func (t *DirectionsTool) Description() string {
	return "Resolve a destination to coordinates and return Google Maps and Apple Maps links " +
		"(a route from the current location when available, otherwise a direct point link). " +
		"If the destination is ambiguous or not found, ask the user for a more specific place " +
		"(city, address, or named landmark) instead of guessing."
}

func (t *DirectionsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"destination": map[string]any{
				"type":        "string",
				"description": "Destination city, address, or named landmark, e.g. 'Stillwater Campground, Utah'",
			},
		},
		"required":             []string{"destination"},
		"additionalProperties": false,
	}
}

func (t *DirectionsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	destination, _ := args["destination"].(string)
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return nil, fmt.Errorf("destination is required")
	}

	place, err := t.geocode(ctx, destination)
	if err != nil {
		return nil, err
	}

	name := displayLocation(place.name, place.country)
	result := map[string]any{
		"destination":     name,
		"latitude":        place.latitude,
		"longitude":       place.longitude,
		"google_maps_url": fmt.Sprintf("https://www.google.com/maps/search/?api=1&query=%s", url.QueryEscape(fmt.Sprintf("%g,%g", place.latitude, place.longitude))),
		"apple_maps_url":  fmt.Sprintf("https://maps.apple.com/?q=%s&ll=%g,%g", url.QueryEscape(name), place.latitude, place.longitude),
	}

	if t.gps != nil && t.gps.Type == "mock" {
		result["origin_latitude"] = t.gps.MockLatitude
		result["origin_longitude"] = t.gps.MockLongitude
		result["google_maps_directions_url"] = fmt.Sprintf(
			"https://www.google.com/maps/dir/?api=1&origin=%g,%g&destination=%g,%g&travelmode=driving",
			t.gps.MockLatitude, t.gps.MockLongitude, place.latitude, place.longitude,
		)
		result["apple_maps_directions_url"] = fmt.Sprintf(
			"https://maps.apple.com/?saddr=%g,%g&daddr=%g,%g&dirflg=d",
			t.gps.MockLatitude, t.gps.MockLongitude, place.latitude, place.longitude,
		)
	}

	return result, nil
}

// directionsPlace is a resolved location. Kept independent from the
// weather tool's geocoding (rather than shared) so each tool's endpoints,
// client, and error wording can evolve separately without coupling them.
type directionsPlace struct {
	name      string
	country   string
	latitude  float64
	longitude float64
}

type directionsGeocodingResponse struct {
	Results []struct {
		Name      string  `json:"name"`
		Country   string  `json:"country"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"results"`
}

type directionsNominatimMatch struct {
	DisplayName string `json:"display_name"`
	Latitude    string `json:"lat"`
	Longitude   string `json:"lon"`
}

func (t *DirectionsTool) geocode(ctx context.Context, destination string) (directionsPlace, error) {
	geocodingURL, err := url.Parse(t.config.GeocodingURL)
	if err != nil {
		return directionsPlace{}, fmt.Errorf("parse geocoding URL: %w", err)
	}
	query := geocodingURL.Query()
	query.Set("name", destination)
	query.Set("count", "1")
	query.Set("language", "en")
	query.Set("format", "json")
	geocodingURL.RawQuery = query.Encode()

	var geocoding directionsGeocodingResponse
	if err := t.getJSON(ctx, geocodingURL.String(), &geocoding); err != nil {
		return directionsPlace{}, fmt.Errorf("geocode destination %q: %w", destination, err)
	}
	if len(geocoding.Results) > 0 {
		result := geocoding.Results[0]
		return directionsPlace{name: result.Name, country: result.Country, latitude: result.Latitude, longitude: result.Longitude}, nil
	}

	return t.resolveLandmark(ctx, destination)
}

func (t *DirectionsTool) resolveLandmark(ctx context.Context, destination string) (directionsPlace, error) {
	if t.config.NominatimURL == "" {
		return directionsPlace{}, fmt.Errorf("destination %q was not found", destination)
	}
	endpoint, err := url.Parse(t.config.NominatimURL)
	if err != nil {
		return directionsPlace{}, fmt.Errorf("parse Nominatim URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("q", destination)
	query.Set("format", "jsonv2")
	query.Set("limit", "1")
	endpoint.RawQuery = query.Encode()

	var matches []directionsNominatimMatch
	if err := t.getJSON(ctx, endpoint.String(), &matches); err != nil {
		return directionsPlace{}, fmt.Errorf("resolve destination %q: %w", destination, err)
	}
	if len(matches) == 0 {
		return directionsPlace{}, fmt.Errorf(
			"destination %q was not found; ask for a more specific place — a city, address, or named landmark",
			destination,
		)
	}

	latitude, err := parseCoordinate(matches[0].Latitude)
	if err != nil {
		return directionsPlace{}, fmt.Errorf("parse latitude for %q: %w", destination, err)
	}
	longitude, err := parseCoordinate(matches[0].Longitude)
	if err != nil {
		return directionsPlace{}, fmt.Errorf("parse longitude for %q: %w", destination, err)
	}
	return directionsPlace{name: matches[0].DisplayName, latitude: latitude, longitude: longitude}, nil
}

func (t *DirectionsTool) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Bosun/0.1")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
