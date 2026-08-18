package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	LLM     LLMConfig     `mapstructure:"llm"`
	MCP     MCPConfig     `mapstructure:"mcp"`
	Sensors SensorsConfig `mapstructure:"sensors"`
	Logging LoggingConfig `mapstructure:"logging"`
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
}

// LocalLLMConfig holds local LLM (Ollama) settings
type LocalLLMConfig struct {
	BaseURL string `mapstructure:"base_url"`
	Model   string `mapstructure:"model"`
	Timeout string `mapstructure:"timeout"`
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

// SensorsConfig holds all sensor configurations
type SensorsConfig struct {
	Weather WeatherConfig `mapstructure:"weather"`
	Fridge  FridgeConfig  `mapstructure:"fridge"`
	GPS     GPSConfig     `mapstructure:"gps"`
	System  SystemConfig  `mapstructure:"system"`
}

// WeatherConfig holds weather sensor settings
type WeatherConfig struct {
	Type         string  `mapstructure:"type"`
	MockTempC    float64 `mapstructure:"mock_temp_c"`
	MockHumidity float64 `mapstructure:"mock_humidity"`
	MQTTTopic    string  `mapstructure:"mqtt_topic"`
	MQTTBroker   string  `mapstructure:"mqtt_broker"`
	HTTPURL      string  `mapstructure:"http_url"`
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
	// LLM defaults
	v.SetDefault("llm.remote.base_url", "https://api.openai.com/v1")
	v.SetDefault("llm.remote.model", "gpt-4o-mini")
	v.SetDefault("llm.remote.api_key_env", "OPENAI_API_KEY")
	v.SetDefault("llm.remote.timeout", "30s")
	v.SetDefault("llm.local.base_url", "http://localhost:11434")
	v.SetDefault("llm.local.model", "llama3.1:8b")
	v.SetDefault("llm.local.timeout", "60s")
	v.SetDefault("llm.router.check_interval", "30s")
	v.SetDefault("llm.router.check_timeout", "5s")
	v.SetDefault("llm.router.check_target", "https://api.openai.com")
	v.SetDefault("llm.router.prefer_remote", true)

	// MCP defaults
	v.SetDefault("mcp.server_name", "smarthelper")
	v.SetDefault("mcp.transport", "stdio")
	v.SetDefault("mcp.log_level", "info")

	// Sensor defaults
	v.SetDefault("sensors.weather.type", "mock")
	v.SetDefault("sensors.weather.mock_temp_c", 22.5)
	v.SetDefault("sensors.weather.mock_humidity", 60.0)
	v.SetDefault("sensors.fridge.type", "mock")
	v.SetDefault("sensors.fridge.mock_fridge_c", 4.0)
	v.SetDefault("sensors.fridge.mock_freezer_c", -18.0)
	v.SetDefault("sensors.gps.type", "mock")
	v.SetDefault("sensors.gps.mock_latitude", 55.7558)
	v.SetDefault("sensors.gps.mock_longitude", 37.6173)
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
