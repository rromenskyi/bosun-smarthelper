package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestDirectionsTool_ResolvesDestinationAndReturnsLinks(t *testing.T) {
	cfg := &config.MapsConfig{GeocodingURL: "https://geocoding.example/search", Timeout: "1s"}
	gpsCfg := &config.GPSConfig{Type: "mock", MockLatitude: 40.4959, MockLongitude: -109.5925}
	tool := NewDirectionsTool(cfg, gpsCfg)
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("name"); got != "Stillwater Campground" {
			t.Errorf("geocoding name = %q, want Stillwater Campground", got)
		}
		body := `{"results":[{"name":"Stillwater Campground","country":"United States","latitude":40.7,"longitude":-109.5}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{"destination": "Stillwater Campground"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	data := result.(map[string]any)

	if data["destination"] != "Stillwater Campground, United States" {
		t.Errorf("destination = %v", data["destination"])
	}
	if data["latitude"] != 40.7 || data["longitude"] != -109.5 {
		t.Errorf("coordinates = %v,%v", data["latitude"], data["longitude"])
	}
	googleURL, _ := data["google_maps_url"].(string)
	if !strings.Contains(googleURL, "google.com/maps") {
		t.Errorf("google_maps_url = %q", googleURL)
	}
	appleURL, _ := data["apple_maps_url"].(string)
	if !strings.Contains(appleURL, "maps.apple.com") {
		t.Errorf("apple_maps_url = %q", appleURL)
	}

	// Mock GPS origin is configured, so directions (not just a point link)
	// should be included.
	directionsURL, _ := data["google_maps_directions_url"].(string)
	if !strings.Contains(directionsURL, "origin=40.4959") || !strings.Contains(directionsURL, "destination=40.7") {
		t.Errorf("google_maps_directions_url = %q", directionsURL)
	}
}

func TestDirectionsTool_AmbiguousDestinationAsksForClarification(t *testing.T) {
	cfg := &config.MapsConfig{GeocodingURL: "https://geocoding.example/search", NominatimURL: "", Timeout: "1s"}
	tool := NewDirectionsTool(cfg, &config.GPSConfig{Type: "mock"})
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
			Request:    req,
		}, nil
	})

	_, err := tool.Execute(context.Background(), map[string]any{"destination": "Springfield"})
	if err == nil {
		t.Fatal("expected an error for an unresolved destination")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want a clarification-style message", err.Error())
	}
}

func TestDirectionsTool_NoOriginWithoutMockGPS(t *testing.T) {
	cfg := &config.MapsConfig{GeocodingURL: "https://geocoding.example/search", Timeout: "1s"}
	tool := NewDirectionsTool(cfg, &config.GPSConfig{Type: "serial"})
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"results":[{"name":"Somewhere","country":"Nowhere","latitude":1.0,"longitude":2.0}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{"destination": "Somewhere"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	data := result.(map[string]any)
	if _, hasDirections := data["google_maps_directions_url"]; hasDirections {
		t.Error("expected no directions URL without a mock GPS origin")
	}
}

func TestDirectionsTool_RequiresDestination(t *testing.T) {
	tool := NewDirectionsTool(&config.MapsConfig{}, &config.GPSConfig{})
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected an error for a missing destination")
	}
}
