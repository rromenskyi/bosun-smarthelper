package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
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

func TestWeatherTool_OpenMeteo(t *testing.T) {
	cfg := &config.WeatherConfig{
		Type:            "open_meteo",
		GeocodingURL:    "https://geocoding.example/search",
		ForecastURL:     "https://forecast.example/forecast",
		DefaultLocation: "Denver",
		Timeout:         "1s",
	}
	tool := NewWeatherTool(cfg)
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Host {
		case "geocoding.example":
			if got := req.URL.Query().Get("name"); got != "Denver" {
				t.Errorf("geocoding name = %q, want Denver", got)
			}
			body = `{"results":[{"name":"Denver","country":"United States","country_code":"US","latitude":39.7392,"longitude":-104.9903}]}`
		case "forecast.example":
			query := req.URL.Query()
			if query.Get("latitude") != "39.7392" || query.Get("longitude") != "-104.9903" {
				t.Errorf("forecast coordinates = %q,%q", query.Get("latitude"), query.Get("longitude"))
			}
			if !strings.Contains(query.Get("current"), "temperature_2m") {
				t.Errorf("current fields = %q, want temperature_2m", query.Get("current"))
			}
			if query.Get("forecast_days") != "7" {
				t.Errorf("forecast_days = %q, want 7", query.Get("forecast_days"))
			}
			body = `{"timezone":"America/Denver","current":{"time":"2026-08-17T12:00","temperature_2m":29.5,"apparent_temperature":30.1,"relative_humidity_2m":31,"precipitation":0,"precipitation_probability":5,"weather_code":1,"wind_speed_10m":12.3,"wind_direction_10m":240,"uv_index":7.2},"daily":{"time":["2026-08-17"],"weather_code":[1],"temperature_2m_max":[31.2],"temperature_2m_min":[18.1],"precipitation_probability_max":[5],"sunrise":["2026-08-17T06:12"],"sunset":["2026-08-17T19:53"]}}`
		default:
			t.Fatalf("unexpected request URL: %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{"forecast_days": float64(7)})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	data := result.(map[string]any)
	if data["location"] != "Denver, United States" {
		t.Errorf("location = %v, want Denver, United States", data["location"])
	}
	if data["temperature_c"] != 29.5 || data["conditions"] != "partly cloudy" {
		t.Errorf("unexpected weather result: %#v", data)
	}
	if data["sunrise"] != "2026-08-17T06:12" || data["source"] != "open-meteo" {
		t.Errorf("unexpected metadata: %#v", data)
	}
	daily := data["daily_forecast"].([]map[string]any)
	if len(daily) != 1 || daily[0]["temperature_max_c"] != 31.2 {
		t.Errorf("unexpected daily forecast: %#v", daily)
	}
}

func TestWeatherTool_OpenMeteoByCoordinatesSkipsGeocoding(t *testing.T) {
	cfg := &config.WeatherConfig{
		Type:        "open_meteo",
		ForecastURL: "https://forecast.example/forecast",
		Timeout:     "1s",
	}
	tool := NewWeatherTool(cfg)
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "forecast.example" {
			t.Fatalf("unexpected request URL: %s — geocoding must be skipped when coordinates are given", req.URL)
		}
		query := req.URL.Query()
		if query.Get("latitude") != "45.5" || query.Get("longitude") != "-122.6" {
			t.Errorf("forecast coordinates = %q,%q, want 45.5,-122.6", query.Get("latitude"), query.Get("longitude"))
		}
		body := `{"timezone":"America/Los_Angeles","current":{"time":"2026-08-17T12:00","temperature_2m":19.5,"weather_code":0}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{"latitude": 45.5, "longitude": -122.6})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	data := result.(map[string]any)
	if data["location"] != "current position" {
		t.Errorf("location = %v, want current position", data["location"])
	}
	if data["latitude"] != 45.5 || data["longitude"] != -122.6 {
		t.Errorf("coordinates = %v,%v, want 45.5,-122.6", data["latitude"], data["longitude"])
	}
	if data["temperature_c"] != 19.5 {
		t.Errorf("temperature_c = %v, want 19.5", data["temperature_c"])
	}
}

func TestWeatherTool_CoordinatesRequireBoth(t *testing.T) {
	tool := NewWeatherTool(&config.WeatherConfig{Type: "open_meteo", DefaultLocation: "Denver"})
	if _, err := tool.Execute(context.Background(), map[string]any{"latitude": 45.5}); err == nil {
		t.Error("expected an error when longitude is missing")
	}
	if _, err := tool.Execute(context.Background(), map[string]any{"longitude": -122.6}); err == nil {
		t.Error("expected an error when latitude is missing")
	}
}

func TestWeatherTool_CoordinatesOutOfRange(t *testing.T) {
	tool := NewWeatherTool(&config.WeatherConfig{Type: "open_meteo"})
	if _, err := tool.Execute(context.Background(), map[string]any{"latitude": 200.0, "longitude": 0.0}); err == nil {
		t.Error("expected an error for an out-of-range latitude")
	}
	if _, err := tool.Execute(context.Background(), map[string]any{"latitude": 0.0, "longitude": -200.0}); err == nil {
		t.Error("expected an error for an out-of-range longitude")
	}
}

func TestWeatherTool_OpenMeteoRequiresLocation(t *testing.T) {
	tool := NewWeatherTool(&config.WeatherConfig{Type: "open_meteo"})
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected missing location error, got nil")
	}
}

func TestWeatherTool_UnsupportedBackend(t *testing.T) {
	tool := NewWeatherTool(&config.WeatherConfig{Type: "mqtt"})

	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected error for unimplemented backend, got nil")
	}
}

type weatherRoundTripFunc func(*http.Request) (*http.Response, error)

func (f weatherRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
