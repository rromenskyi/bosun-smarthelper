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

// WeatherTool provides outdoor weather data
type WeatherTool struct {
	config *config.WeatherConfig
	client *http.Client
}

// NewWeatherTool creates a new weather tool
func NewWeatherTool(cfg *config.WeatherConfig) *WeatherTool {
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &WeatherTool{config: cfg, client: &http.Client{Timeout: timeout}}
}

func (t *WeatherTool) Name() string {
	return "get_weather"
}

// RequiresNetwork reports whether the selected weather backend uses an
// internet service. Mock and future local sensor backends remain available.
func (t *WeatherTool) RequiresNetwork() bool {
	return t.config.Type == "open_meteo"
}

func (t *WeatherTool) Description() string {
	return "Get current conditions or a daily forecast for a city, postal code, or named landmark. For mountain weather, use a specific mountain, park, or pass; never substitute a nearby city. For \"what's the weather here/where I am\", call get_gps first and pass its latitude/longitude directly instead of guessing a place name — exact and skips geocoding."
}

func (t *WeatherTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{
				"type":        "string",
				"description": "City, postal code, or specific named landmark with regional context, such as 'Rocky Mountain National Park Colorado' (optional; uses the configured default location when omitted). Ignored if latitude/longitude are given.",
			},
			"latitude": map[string]any{
				"type":        "number",
				"minimum":     -90,
				"maximum":     90,
				"description": "Exact latitude, e.g. from get_gps — use together with longitude instead of location when you already know exactly where you are.",
			},
			"longitude": map[string]any{
				"type":        "number",
				"minimum":     -180,
				"maximum":     180,
				"description": "Exact longitude, e.g. from get_gps — must be given together with latitude.",
			},
			"forecast_days": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     16,
				"description": "Number of forecast days to return. Use 1 for current weather and up to 16 for future questions.",
			},
		},
		"additionalProperties": false,
	}
}

func (t *WeatherTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	location := strings.TrimSpace(t.config.DefaultLocation)
	if loc, ok := args["location"].(string); ok && loc != "" {
		location = strings.TrimSpace(loc)
	}

	// For mock type, return configured values
	if t.config.Type == "mock" {
		if location == "" {
			location = "current"
		}
		return map[string]any{
			"location":      location,
			"temperature_c": t.config.MockTempC,
			"humidity":      t.config.MockHumidity,
			"unit":          "celsius",
			"source":        "mock",
		}, nil
	}
	if t.config.Type == "open_meteo" {
		forecastDays, err := parseForecastDays(args["forecast_days"])
		if err != nil {
			return nil, err
		}
		place, hasCoordinates, err := coordinatesFromArgs(args)
		if err != nil {
			return nil, err
		}
		if hasCoordinates {
			return t.forecastForPlace(ctx, place, forecastDays)
		}
		if location == "" {
			return nil, fmt.Errorf("weather location is required when default_location is not configured")
		}
		place, err = t.resolvePlace(ctx, location)
		if err != nil {
			return nil, err
		}
		return t.forecastForPlace(ctx, place, forecastDays)
	}

	// TODO: Implement MQTT, HTTP, 1-Wire backends
	return nil, fmt.Errorf("weather sensor type %q not implemented", t.config.Type)
}

type openMeteoGeocodingResponse struct {
	Results []weatherPlace `json:"results"`
}

type weatherPlace struct {
	Name        string  `json:"name"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type nominatimPlace struct {
	DisplayName string `json:"display_name"`
	Latitude    string `json:"lat"`
	Longitude   string `json:"lon"`
}

type openMeteoForecastResponse struct {
	Timezone string `json:"timezone"`
	Current  struct {
		Time                     string  `json:"time"`
		Temperature              float64 `json:"temperature_2m"`
		ApparentTemperature      float64 `json:"apparent_temperature"`
		RelativeHumidity         float64 `json:"relative_humidity_2m"`
		Precipitation            float64 `json:"precipitation"`
		PrecipitationProbability float64 `json:"precipitation_probability"`
		WeatherCode              int     `json:"weather_code"`
		WindSpeed                float64 `json:"wind_speed_10m"`
		WindDirection            float64 `json:"wind_direction_10m"`
		UVIndex                  float64 `json:"uv_index"`
	} `json:"current"`
	Daily struct {
		Time                     []string  `json:"time"`
		WeatherCode              []int     `json:"weather_code"`
		TemperatureMax           []float64 `json:"temperature_2m_max"`
		TemperatureMin           []float64 `json:"temperature_2m_min"`
		ApparentTemperatureMax   []float64 `json:"apparent_temperature_max"`
		ApparentTemperatureMin   []float64 `json:"apparent_temperature_min"`
		PrecipitationSum         []float64 `json:"precipitation_sum"`
		PrecipitationProbability []float64 `json:"precipitation_probability_max"`
		WindSpeedMax             []float64 `json:"wind_speed_10m_max"`
		UVIndexMax               []float64 `json:"uv_index_max"`
		Sunrise                  []string  `json:"sunrise"`
		Sunset                   []string  `json:"sunset"`
	} `json:"daily"`
}

// resolvePlace turns a free-text location into coordinates: Open-Meteo's
// own geocoder first, falling back to Nominatim for named landmarks (parks,
// passes, mountains) it doesn't cover. Skipped entirely when the caller
// already has exact coordinates — see coordinatesFromArgs.
func (t *WeatherTool) resolvePlace(ctx context.Context, location string) (weatherPlace, error) {
	geocodingURL, err := url.Parse(t.config.GeocodingURL)
	if err != nil {
		return weatherPlace{}, fmt.Errorf("parse Open-Meteo geocoding URL: %w", err)
	}
	query := geocodingURL.Query()
	query.Set("name", location)
	query.Set("count", "1")
	query.Set("language", "en")
	query.Set("format", "json")
	geocodingURL.RawQuery = query.Encode()

	var geocoding openMeteoGeocodingResponse
	if err := t.getJSON(ctx, geocodingURL.String(), &geocoding); err != nil {
		return weatherPlace{}, fmt.Errorf("geocode weather location %q: %w", location, err)
	}
	if len(geocoding.Results) > 0 {
		return geocoding.Results[0], nil
	}
	return t.resolveLandmark(ctx, location)
}

// coordinatesFromArgs reads latitude/longitude from args, if both are
// present — the caller (e.g. get_gps's own result) already knows exactly
// where it is, so geocoding a place name back into coordinates would only
// add a lossy round trip. Both must be given together; either alone is
// almost certainly a mistake, not a valid position.
func coordinatesFromArgs(args map[string]any) (weatherPlace, bool, error) {
	latitude, hasLatitude := args["latitude"].(float64)
	longitude, hasLongitude := args["longitude"].(float64)
	if !hasLatitude && !hasLongitude {
		return weatherPlace{}, false, nil
	}
	if !hasLatitude || !hasLongitude {
		return weatherPlace{}, false, fmt.Errorf("latitude and longitude must both be given together")
	}
	if latitude < -90 || latitude > 90 {
		return weatherPlace{}, false, fmt.Errorf("latitude %g is out of range", latitude)
	}
	if longitude < -180 || longitude > 180 {
		return weatherPlace{}, false, fmt.Errorf("longitude %g is out of range", longitude)
	}
	return weatherPlace{Name: "current position", Latitude: latitude, Longitude: longitude}, true, nil
}

// forecastForPlace fetches current conditions and (for forecastDays > 1) a
// daily forecast for an already-resolved place — shared by both the
// geocoded-location path and the direct-coordinates path.
func (t *WeatherTool) forecastForPlace(ctx context.Context, place weatherPlace, forecastDays int) (any, error) {
	forecastURL, err := url.Parse(t.config.ForecastURL)
	if err != nil {
		return nil, fmt.Errorf("parse Open-Meteo forecast URL: %w", err)
	}
	query := forecastURL.Query()
	query.Set("latitude", fmt.Sprintf("%g", place.Latitude))
	query.Set("longitude", fmt.Sprintf("%g", place.Longitude))
	query.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,precipitation_probability,weather_code,wind_speed_10m,wind_direction_10m,uv_index")
	query.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,apparent_temperature_max,apparent_temperature_min,precipitation_sum,precipitation_probability_max,wind_speed_10m_max,uv_index_max,sunrise,sunset")
	query.Set("forecast_days", fmt.Sprintf("%d", forecastDays))
	query.Set("timezone", "auto")
	forecastURL.RawQuery = query.Encode()

	var forecast openMeteoForecastResponse
	if err := t.getJSON(ctx, forecastURL.String(), &forecast); err != nil {
		return nil, fmt.Errorf("get weather for %q: %w", place.Name, err)
	}

	result := map[string]any{
		"location":                      displayLocation(place.Name, place.Country),
		"country_code":                  place.CountryCode,
		"latitude":                      place.Latitude,
		"longitude":                     place.Longitude,
		"timezone":                      forecast.Timezone,
		"time":                          forecast.Current.Time,
		"temperature_c":                 forecast.Current.Temperature,
		"apparent_temperature_c":        forecast.Current.ApparentTemperature,
		"humidity_pct":                  forecast.Current.RelativeHumidity,
		"precipitation_mm":              forecast.Current.Precipitation,
		"precipitation_probability_pct": forecast.Current.PrecipitationProbability,
		"uv_index":                      forecast.Current.UVIndex,
		"wind_kmh":                      forecast.Current.WindSpeed,
		"wind_direction_deg":            forecast.Current.WindDirection,
		"weather_code":                  forecast.Current.WeatherCode,
		"conditions":                    weatherCodeDescription(forecast.Current.WeatherCode),
		"source":                        "open-meteo",
	}
	if len(forecast.Daily.Sunrise) > 0 {
		result["sunrise"] = forecast.Daily.Sunrise[0]
	}
	if len(forecast.Daily.Sunset) > 0 {
		result["sunset"] = forecast.Daily.Sunset[0]
	}
	if forecastDays > 1 {
		result["daily_forecast"] = dailyForecast(forecast)
	}
	return result, nil
}

func (t *WeatherTool) resolveLandmark(ctx context.Context, location string) (weatherPlace, error) {
	if t.config.NominatimURL == "" {
		return weatherPlace{}, fmt.Errorf("weather location %q was not found", location)
	}
	endpoint, err := url.Parse(t.config.NominatimURL)
	if err != nil {
		return weatherPlace{}, fmt.Errorf("parse Nominatim URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("q", location)
	query.Set("format", "jsonv2")
	query.Set("limit", "1")
	endpoint.RawQuery = query.Encode()
	var matches []nominatimPlace
	if err := t.getJSON(ctx, endpoint.String(), &matches); err != nil {
		return weatherPlace{}, fmt.Errorf("resolve weather landmark %q: %w", location, err)
	}
	if len(matches) == 0 {
		return weatherPlace{}, fmt.Errorf("weather location %q was not found; provide a city or a specific named mountain, park, or pass", location)
	}
	latitude, err := parseCoordinate(matches[0].Latitude)
	if err != nil {
		return weatherPlace{}, fmt.Errorf("parse latitude for %q: %w", location, err)
	}
	longitude, err := parseCoordinate(matches[0].Longitude)
	if err != nil {
		return weatherPlace{}, fmt.Errorf("parse longitude for %q: %w", location, err)
	}
	return weatherPlace{Name: matches[0].DisplayName, Latitude: latitude, Longitude: longitude}, nil
}

func dailyForecast(forecast openMeteoForecastResponse) []map[string]any {
	days := make([]map[string]any, 0, len(forecast.Daily.Time))
	for i, date := range forecast.Daily.Time {
		day := map[string]any{"date": date}
		addDailyValue(day, "weather_code", forecast.Daily.WeatherCode, i)
		if code, ok := day["weather_code"].(int); ok {
			day["conditions"] = weatherCodeDescription(code)
		}
		addDailyValue(day, "temperature_max_c", forecast.Daily.TemperatureMax, i)
		addDailyValue(day, "temperature_min_c", forecast.Daily.TemperatureMin, i)
		addDailyValue(day, "apparent_temperature_max_c", forecast.Daily.ApparentTemperatureMax, i)
		addDailyValue(day, "apparent_temperature_min_c", forecast.Daily.ApparentTemperatureMin, i)
		addDailyValue(day, "precipitation_sum_mm", forecast.Daily.PrecipitationSum, i)
		addDailyValue(day, "precipitation_probability_max_pct", forecast.Daily.PrecipitationProbability, i)
		addDailyValue(day, "wind_speed_max_kmh", forecast.Daily.WindSpeedMax, i)
		addDailyValue(day, "uv_index_max", forecast.Daily.UVIndexMax, i)
		addDailyValue(day, "sunrise", forecast.Daily.Sunrise, i)
		addDailyValue(day, "sunset", forecast.Daily.Sunset, i)
		days = append(days, day)
	}
	return days
}

func addDailyValue[T any](day map[string]any, name string, values []T, index int) {
	if index < len(values) {
		day[name] = values[index]
	}
}

func parseForecastDays(value any) (int, error) {
	if value == nil {
		return 1, nil
	}
	var days int
	switch typed := value.(type) {
	case int:
		days = typed
	case float64:
		days = int(typed)
		if float64(days) != typed {
			return 0, fmt.Errorf("forecast_days must be an integer")
		}
	default:
		return 0, fmt.Errorf("forecast_days must be an integer")
	}
	if days < 1 || days > 16 {
		return 0, fmt.Errorf("forecast_days must be between 1 and 16")
	}
	return days, nil
}

func parseCoordinate(value string) (float64, error) {
	var coordinate float64
	if _, err := fmt.Sscan(value, &coordinate); err != nil {
		return 0, err
	}
	return coordinate, nil
}

func (t *WeatherTool) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "ai-local-smarthelper/1.0")
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

func displayLocation(name, country string) string {
	if country == "" {
		return name
	}
	return name + ", " + country
}

func weatherCodeDescription(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1, 2, 3:
		return "partly cloudy"
	case 45, 48:
		return "fog"
	case 51, 53, 55, 56, 57:
		return "drizzle"
	case 61, 63, 65, 66, 67:
		return "rain"
	case 71, 73, 75, 77:
		return "snow"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95, 96, 99:
		return "thunderstorm"
	default:
		return "unknown"
	}
}
