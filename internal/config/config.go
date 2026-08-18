package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	Assistant AssistantConfig `mapstructure:"assistant"`
	LLM       LLMConfig       `mapstructure:"llm"`
	MCP       MCPConfig       `mapstructure:"mcp"`
	Web       WebConfig       `mapstructure:"web"`
	Memo      MemoConfig      `mapstructure:"memo"`
	Online    OnlineConfig    `mapstructure:"online_tools"`
	Sensors   SensorsConfig   `mapstructure:"sensors"`
	Logging   LoggingConfig   `mapstructure:"logging"`
}

// AssistantConfig holds user-facing identity and optional response style.
type AssistantConfig struct {
	NameRU      string `mapstructure:"name_ru"`
	NameEN      string `mapstructure:"name_en"`
	StylePrompt string `mapstructure:"style_prompt"`
}

// MemoConfig holds persistent local memo settings.
type MemoConfig struct {
	Path string `mapstructure:"path"`
}

// OnlineConfig holds endpoints and timeouts for public internet tools.
type OnlineConfig struct {
	DuckDuckGoURL  string `mapstructure:"duckduckgo_url"`
	WikipediaURL   string `mapstructure:"wikipedia_url"`
	RequestTimeout string `mapstructure:"request_timeout"`
}

// LLMConfig holds LLM-related configuration
type LLMConfig struct {
	Remote RemoteLLMConfig `mapstructure:"remote"`
	Local  LocalLLMConfig  `mapstructure:"local"`
	Router RouterConfig    `mapstructure:"router"`
}

// RemoteLLMConfig holds remote LLM (OpenAI-compatible) settings
type RemoteLLMConfig struct {
	BaseURL      string `mapstructure:"base_url"`
	Model        string `mapstructure:"model"`
	APIKeyEnv    string `mapstructure:"api_key_env"`
	Organization string `mapstructure:"organization"`
	Timeout      string `mapstructure:"timeout"`
	MaxRetries   int    `mapstructure:"max_retries"`
	RetryBackoff string `mapstructure:"retry_backoff"`
}

// LocalLLMConfig holds local LLM (Ollama) settings
type LocalLLMConfig struct {
	BaseURL       string `mapstructure:"base_url"`
	Model         string `mapstructure:"model"`
	APIFormat     string `mapstructure:"api_format"`
	APIKeyEnv     string `mapstructure:"api_key_env"`
	SupportsTools bool   `mapstructure:"supports_tools"`
	Timeout       string `mapstructure:"timeout"`
}

// RouterConfig holds LLM router settings
type RouterConfig struct {
	CheckInterval string `mapstructure:"check_interval"`
	CheckTimeout  string `mapstructure:"check_timeout"`
	CheckTarget   string `mapstructure:"check_target"`
	PreferRemote  bool   `mapstructure:"prefer_remote"`
}

// MCPConfig holds MCP server settings
type MCPConfig struct {
	ServerName string `mapstructure:"server_name"`
	Transport  string `mapstructure:"transport"`
	LogLevel   string `mapstructure:"log_level"`
}

// WebConfig holds settings for the LAN-only web interface.
type WebConfig struct {
	Bind            string `mapstructure:"bind"`
	RequestTimeout  string `mapstructure:"request_timeout"`
	DefaultLanguage string `mapstructure:"default_language"`
	HistoryTurns    int    `mapstructure:"history_turns"`
	HistoryMaxChars int    `mapstructure:"history_max_chars"`
	SessionTTL      string `mapstructure:"session_ttl"`
	MaxSessions     int    `mapstructure:"max_sessions"`
}

// SensorsConfig holds all sensor configurations
type SensorsConfig struct {
	Weather WeatherConfig `mapstructure:"weather"`
	Fridge  FridgeConfig  `mapstructure:"fridge"`
	GPS     GPSConfig     `mapstructure:"gps"`
	System  SystemConfig  `mapstructure:"system"`
}

// WeatherConfig holds weather sensor settings
type WeatherConfig struct {
	Type            string  `mapstructure:"type"`
	MockTempC       float64 `mapstructure:"mock_temp_c"`
	MockHumidity    float64 `mapstructure:"mock_humidity"`
	MQTTTopic       string  `mapstructure:"mqtt_topic"`
	MQTTBroker      string  `mapstructure:"mqtt_broker"`
	HTTPURL         string  `mapstructure:"http_url"`
	GeocodingURL    string  `mapstructure:"geocoding_url"`
	NominatimURL    string  `mapstructure:"nominatim_url"`
	ForecastURL     string  `mapstructure:"forecast_url"`
	DefaultLocation string  `mapstructure:"default_location"`
	Timeout         string  `mapstructure:"timeout"`
}

// FridgeConfig holds fridge sensor settings
type FridgeConfig struct {
	Type         string  `mapstructure:"type"`
	MockFridgeC  float64 `mapstructure:"mock_fridge_c"`
	MockFreezerC float64 `mapstructure:"mock_freezer_c"`
	MQTTTopic    string  `mapstructure:"mqtt_topic"`
}

// GPSConfig holds GPS sensor settings
type GPSConfig struct {
	Type          string  `mapstructure:"type"`
	MockLatitude  float64 `mapstructure:"mock_latitude"`
	MockLongitude float64 `mapstructure:"mock_longitude"`
	MockSpeedKMH  float64 `mapstructure:"mock_speed_kmh"`
	MockAltitudeM float64 `mapstructure:"mock_altitude_m"`
	SerialPort    string  `mapstructure:"serial_port"`
	BaudRate      int     `mapstructure:"baud_rate"`
}

// SystemConfig holds system sensor settings
type SystemConfig struct {
	Type    string   `mapstructure:"type"`
	Include []string `mapstructure:"include"`
}

// LoggingConfig holds logging settings
type LoggingConfig struct {
	Level    string `mapstructure:"level"`
	Format   string `mapstructure:"format"`
	Output   string `mapstructure:"output"`
	FilePath string `mapstructure:"file_path"`
}

// Load reads configuration from file and environment variables
func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	// Config search paths
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.config/smarthelper")
	v.AddConfigPath("/etc/smarthelper")

	// Environment variable overrides
	v.SetEnvPrefix("SMARTHELPER")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Set defaults
	setDefaults(v)

	// Read config file (optional)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
		// Config file not found - using defaults + env vars
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// Assistant identity
	v.SetDefault("assistant.name_ru", "Старпом")
	v.SetDefault("assistant.name_en", "Bosun")
	v.SetDefault("assistant.style_prompt", "")

	// LLM defaults
	v.SetDefault("llm.remote.base_url", "https://api.openai.com/v1")
	v.SetDefault("llm.remote.model", "gpt-4o-mini")
	v.SetDefault("llm.remote.api_key_env", "OPENAI_API_KEY")
	v.SetDefault("llm.remote.timeout", "30s")
	v.SetDefault("llm.remote.max_retries", 5)
	v.SetDefault("llm.remote.retry_backoff", "500ms")
	v.SetDefault("llm.local.base_url", "http://localhost:11434")
	v.SetDefault("llm.local.model", "llama3.1:8b")
	v.SetDefault("llm.local.api_format", "ollama")
	v.SetDefault("llm.local.api_key_env", "")
	v.SetDefault("llm.local.supports_tools", true)
	v.SetDefault("llm.local.timeout", "60s")
	v.SetDefault("llm.router.check_interval", "30s")
	v.SetDefault("llm.router.check_timeout", "5s")
	v.SetDefault("llm.router.check_target", "https://api.openai.com")
	v.SetDefault("llm.router.prefer_remote", true)

	// MCP defaults
	v.SetDefault("mcp.server_name", "bosun")
	v.SetDefault("mcp.transport", "stdio")
	v.SetDefault("mcp.log_level", "info")

	// Web defaults bind only to loopback. LAN deployments should use the
	// machine's explicit private address rather than 0.0.0.0.
	v.SetDefault("web.bind", "127.0.0.1:8080")
	v.SetDefault("web.request_timeout", "180s")
	v.SetDefault("web.default_language", "ru")
	v.SetDefault("web.history_turns", 8)
	v.SetDefault("web.history_max_chars", 12000)
	v.SetDefault("web.session_ttl", "24h")
	v.SetDefault("web.max_sessions", 100)

	// Public, keyless online tools
	v.SetDefault("online_tools.duckduckgo_url", "https://html.duckduckgo.com/html/")
	v.SetDefault("online_tools.wikipedia_url", "https://{lang}.wikipedia.org/api/rest_v1/page/summary/{title}")
	v.SetDefault("online_tools.request_timeout", "10s")

	// Sensor defaults
	v.SetDefault("sensors.weather.type", "mock")
	v.SetDefault("sensors.weather.mock_temp_c", 22.5)
	v.SetDefault("sensors.weather.mock_humidity", 60.0)
	v.SetDefault("sensors.weather.geocoding_url", "https://geocoding-api.open-meteo.com/v1/search")
	v.SetDefault("sensors.weather.nominatim_url", "https://nominatim.openstreetmap.org/search")
	v.SetDefault("sensors.weather.forecast_url", "https://api.open-meteo.com/v1/forecast")
	v.SetDefault("sensors.weather.timeout", "8s")
	v.SetDefault("sensors.fridge.type", "mock")
	v.SetDefault("sensors.fridge.mock_fridge_c", 4.0)
	v.SetDefault("sensors.fridge.mock_freezer_c", -18.0)
	v.SetDefault("sensors.gps.type", "mock")
	v.SetDefault("sensors.gps.mock_latitude", 40.7608)
	v.SetDefault("sensors.gps.mock_longitude", -111.8910)
	v.SetDefault("sensors.gps.mock_speed_kmh", 0.0)
	v.SetDefault("sensors.gps.mock_altitude_m", 150.0)
	v.SetDefault("sensors.system.type", "native")

	// Logging defaults
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("logging.output", "stderr")
}

// GetConfigPath returns the path to the config file
func GetConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "smarthelper", "config.yaml")
}
