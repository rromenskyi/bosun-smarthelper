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
	Documents DocumentsConfig `mapstructure:"documents"`
	ErrorLog  ErrorLogConfig  `mapstructure:"error_log"`
	Online    OnlineConfig    `mapstructure:"online_tools"`
	Maps      MapsConfig      `mapstructure:"maps"`
	Sensors   SensorsConfig   `mapstructure:"sensors"`
	Logging   LoggingConfig   `mapstructure:"logging"`
	Voice     VoiceConfig     `mapstructure:"voice"`
	Metrics   MetricsConfig   `mapstructure:"metrics"`
}

// MetricsConfig holds the local monitoring dashboard's sampling and
// retention settings (internal/metrics, docs/monitoring.md) — a personal,
// bounded-history analog to MRTG/Grafana, not a general observability
// stack.
type MetricsConfig struct {
	// Enabled defaults to true — sampling a handful of already-available
	// sensors every few seconds is cheap, and the dashboard has nothing to
	// show without it.
	Enabled bool `mapstructure:"enabled"`
	// Interval between samples, e.g. "30s".
	Interval string `mapstructure:"interval"`
	// RetentionDays bounds the store's size, same idea as MRTG's fixed
	// rrd file — old samples are pruned rather than kept forever.
	RetentionDays int `mapstructure:"retention_days"`
	// StorePath is the SQLite file's location. Empty uses
	// metrics.DefaultPath() (~/.local/share/bosun/metrics.db).
	StorePath string `mapstructure:"store_path"`
	// Sources declares what to sample — deliberately data, not code, so a
	// new sensor (e.g. a battery/water-tank reading once that hardware
	// exists) is a config.yaml addition, not a Go change. Defaults cover
	// every sensor this project ships with; see the default value set in
	// setDefaults and docs/monitoring.md for the exact shape.
	Sources []MetricSource `mapstructure:"sources"`
}

// MetricSource is one row of the metrics dashboard's "what to show, from
// where, how to parse it" table.
type MetricSource struct {
	// Metric is the name samples are stored/queried under (internal/metrics).
	Metric string `mapstructure:"metric"`
	// Tool is a registered tool name (internal/tools.Registry) — the exact
	// same tool the chat agent calls, so a sensor only needs implementing
	// once.
	Tool string `mapstructure:"tool"`
	// Args, if set, are passed to the tool's Execute as-is (same shape as
	// its InputSchema — e.g. {"include": ["cpu"]} for get_system_info).
	Args map[string]any `mapstructure:"args"`
	// Field is a dot-separated path into the tool's map[string]any result,
	// e.g. "memory.used_percent" for a nested field, or "fridge_c" for a
	// top-level one.
	Field string `mapstructure:"field"`
	// Aggregate is "" (Field must resolve to a single number) or "avg"
	// (Field must resolve to a []float64, e.g. per-core cpu_percent,
	// averaged into one sample).
	Aggregate string `mapstructure:"aggregate"`
	// LabelRU/LabelEN/Unit are shown in the dashboard's metric picker and
	// chart headers.
	LabelRU string `mapstructure:"label_ru"`
	LabelEN string `mapstructure:"label_en"`
	Unit    string `mapstructure:"unit"`
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
	// CanonicalTags, if non-empty, enables background tag normalization
	// (see internal/tools' NormalizeTags and docs/memo-search.md): memos
	// with free-form tags get mapped onto this fixed vocabulary, added
	// alongside (never replacing) the original tags. Empty disables the
	// feature entirely — no background LLM calls are made.
	CanonicalTags []string `mapstructure:"canonical_tags"`
	// TagNormalizeInterval is how often the background pass checks for
	// work. Defaults to 5m. Each check only runs if no chat request is in
	// flight (see webui.Server.TryIdle), so a busy assistant never falls
	// behind — normalization just waits for the next quiet interval.
	TagNormalizeInterval string `mapstructure:"tag_normalize_interval"`
	// MinSearchRelevance filters out weak matches (from both memos and
	// uploaded documents) before they ever reach the LLM — cosine
	// similarity, roughly [-1, 1]. 0 (or below) disables filtering
	// entirely. See docs/memo-search.md.
	MinSearchRelevance float64 `mapstructure:"min_search_relevance"`
}

// DocumentsConfig holds settings for uploaded reference documents (see
// internal/documents and docs/memo-search.md). Empty Path resolves to
// ~/.local/share/bosun/documents.json.
type DocumentsConfig struct {
	Path string `mapstructure:"path"`
}

// VoiceConfig holds settings for the voice interface — see docs/voice.md.
// TTS.ModelPath empty disables /api/tts; STT.BaseURL empty disables
// /api/stt. Independent of each other.
type VoiceConfig struct {
	TTS TTSConfig `mapstructure:"tts"`
	STT STTConfig `mapstructure:"stt"`
}

// TTSConfig points at a built `piper_exe` (patched to emit 16-bit PCM
// WAV — see deploy/piper/wav-pcm16.patch) and a voice model. All three
// paths are required together; ModelPath empty disables the feature.
type TTSConfig struct {
	BinaryPath     string `mapstructure:"binary_path"`
	ModelPath      string `mapstructure:"model_path"`
	EspeakDataPath string `mapstructure:"espeak_data_path"`
}

// STTConfig points at a running `whisper-server` (see
// deploy/whisper/Dockerfile) — a separate long-running process, unlike
// TTS's per-request subprocess, since whisper.cpp's model load time is
// significant enough to be worth keeping resident. BaseURL empty (the
// default) disables /api/stt entirely.
type STTConfig struct {
	BaseURL  string `mapstructure:"base_url"`
	Language string `mapstructure:"language"`
}

// ErrorLogConfig holds settings for the tool/LLM failure log used to drive
// an improvement loop (see internal/errlog). Empty Path resolves to
// errlog.DefaultPath() (~/.local/share/bosun/errors.jsonl).
type ErrorLogConfig struct {
	Path string `mapstructure:"path"`
}

// OnlineConfig holds endpoints and timeouts for public internet tools.
type OnlineConfig struct {
	DuckDuckGoURL  string `mapstructure:"duckduckgo_url"`
	WikipediaURL   string `mapstructure:"wikipedia_url"`
	RequestTimeout string `mapstructure:"request_timeout"`
}

// MapsConfig holds geocoding endpoints for the get_directions tool.
type MapsConfig struct {
	GeocodingURL string `mapstructure:"geocoding_url"`
	NominatimURL string `mapstructure:"nominatim_url"`
	Timeout      string `mapstructure:"timeout"`
}

// LLMConfig holds LLM-related configuration
type LLMConfig struct {
	Remote     RemoteLLMConfig  `mapstructure:"remote"`
	Local      LocalLLMConfig   `mapstructure:"local"`
	Router     RouterConfig     `mapstructure:"router"`
	Embeddings EmbeddingsConfig `mapstructure:"embeddings"`
}

// EmbeddingsConfig holds an OpenAI-compatible /embeddings endpoint used for
// memo semantic search (see internal/tools/memo.go). An empty BaseURL
// disables the feature: memos still save normally, just without a vector
// to rank against, and "search" degrades to plain substring matching.
type EmbeddingsConfig struct {
	BaseURL   string `mapstructure:"base_url"`
	Model     string `mapstructure:"model"`
	APIKeyEnv string `mapstructure:"api_key_env"`
	Timeout   string `mapstructure:"timeout"`
}

// RemoteLLMConfig holds remote LLM (OpenAI-compatible) settings
type RemoteLLMConfig struct {
	BaseURL      string  `mapstructure:"base_url"`
	Model        string  `mapstructure:"model"`
	APIKeyEnv    string  `mapstructure:"api_key_env"`
	Organization string  `mapstructure:"organization"`
	Temperature  float64 `mapstructure:"temperature"`
	Timeout      string  `mapstructure:"timeout"`
	MaxRetries   int     `mapstructure:"max_retries"`
	RetryBackoff string  `mapstructure:"retry_backoff"`
}

// LocalLLMConfig holds local LLM (Ollama) settings
type LocalLLMConfig struct {
	BaseURL       string  `mapstructure:"base_url"`
	Model         string  `mapstructure:"model"`
	APIFormat     string  `mapstructure:"api_format"`
	APIKeyEnv     string  `mapstructure:"api_key_env"`
	SupportsTools bool    `mapstructure:"supports_tools"`
	Temperature   float64 `mapstructure:"temperature"`
	Timeout       string  `mapstructure:"timeout"`
	// Stream defaults to true. Some llama.cpp builds/models corrupt
	// multi-byte UTF-8 (e.g. Cyrillic) specifically in streaming mode —
	// observed with Gemma + --skip-chat-parsing, confirmed absent in
	// buffered (stream:false) mode against the same server. Set false as an
	// escape hatch if a local server exhibits this; see docs/streaming.md.
	Stream bool `mapstructure:"stream"`
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
	Bind            string        `mapstructure:"bind"`
	RequestTimeout  string        `mapstructure:"request_timeout"`
	DefaultLanguage string        `mapstructure:"default_language"`
	History         HistoryConfig `mapstructure:"history"`
	SessionTTL      string        `mapstructure:"session_ttl"`
	MaxSessions     int           `mapstructure:"max_sessions"`
	// SessionStorePath persists chat sessions to disk so history survives a
	// page reload and a service restart. Empty disables persistence
	// (in-memory only).
	SessionStorePath string `mapstructure:"session_store_path"`
	// SettingsStorePath persists the settings page's live-editable values
	// (persona/style prompt, default language, LLM temperatures, memo tag
	// canonicalization vocabulary — see docs/settings.md). Empty uses
	// ~/.local/share/bosun/settings.json. Once this file exists it is the
	// source of truth for those fields — config.yaml's own values only
	// seed it the first time.
	SettingsStorePath string `mapstructure:"settings_store_path"`
	// TLSCertFile and TLSKeyFile enable HTTPS when both are set (e.g.
	// certs from mkcert — see docs/tls.md). Empty (the default) serves
	// plain HTTP, matching every LAN-only deployment before this option
	// existed.
	TLSCertFile string `mapstructure:"tls_cert_file"`
	TLSKeyFile  string `mapstructure:"tls_key_file"`
	// CACertFile, when set, is served at GET /ca.pem (and linked from the
	// settings page) so a new phone/laptop can download and trust the CA
	// that issued TLSCertFile without a separate file transfer — see
	// docs/tls.md. Never point this at a CA's private key, only its public
	// cert (mkcert's rootCA.pem, not rootCA-key.pem).
	CACertFile string `mapstructure:"ca_cert_file"`
	// HTTPFallbackBind, when TLS is enabled, additionally serves plain HTTP
	// (same handler) on this address — for a device that can't be made to
	// trust the TLS cert at all (e.g. a corporate MDM-managed phone that
	// blocks installing custom root certs). Ignored unless TLSCertFile and
	// TLSKeyFile are both set. Empty (the default) serves TLS only. See
	// docs/tls.md.
	HTTPFallbackBind string `mapstructure:"http_fallback_bind"`
}

// HistoryConfig holds separate chat-history budgets per provider: a weak
// local fallback model needs a small window, while a remote model's context
// is effectively unlimited by comparison. See docs/token-budget.md.
type HistoryConfig struct {
	Local  HistoryLimits `mapstructure:"local"`
	Remote HistoryLimits `mapstructure:"remote"`
}

// HistoryLimits bounds retained conversation turns for one provider.
type HistoryLimits struct {
	Turns    int `mapstructure:"turns"`
	MaxChars int `mapstructure:"max_chars"`
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
	v.SetDefault("llm.remote.temperature", 0.8)
	v.SetDefault("llm.remote.timeout", "30s")
	v.SetDefault("llm.remote.max_retries", 5)
	v.SetDefault("llm.remote.retry_backoff", "500ms")
	v.SetDefault("llm.local.base_url", "http://localhost:11434")
	v.SetDefault("llm.local.model", "llama3.1:8b")
	v.SetDefault("llm.local.api_format", "ollama")
	v.SetDefault("llm.local.api_key_env", "")
	v.SetDefault("llm.local.supports_tools", true)
	// Lower than remote: a weak local model is already shakier on
	// tool-call/format compliance (see stripLeakedReasoningMarker), so less
	// randomness matters more here than response variety.
	v.SetDefault("llm.local.temperature", 0.55)
	v.SetDefault("llm.local.stream", true)
	v.SetDefault("llm.local.timeout", "60s")
	v.SetDefault("llm.router.check_interval", "30s")
	v.SetDefault("llm.router.check_timeout", "5s")
	v.SetDefault("llm.router.check_target", "https://api.openai.com")
	v.SetDefault("llm.router.prefer_remote", true)
	v.SetDefault("llm.embeddings.base_url", "")
	v.SetDefault("llm.embeddings.timeout", "10s")
	v.SetDefault("memo.tag_normalize_interval", "5m")
	// Below this cosine similarity, a memo/document search hit is noise,
	// not a real answer — filtered out before it ever reaches the LLM.
	// See docs/memo-search.md.
	v.SetDefault("memo.min_search_relevance", 0.4)

	// Metrics defaults — see docs/monitoring.md. This default source list
	// covers every sensor this project ships with today; a deployment
	// with more hardware (battery, water tank, ...) appends to it in
	// config.yaml rather than replacing it, unless it explicitly wants to
	// drop one of these.
	v.SetDefault("metrics.enabled", true)
	v.SetDefault("metrics.interval", "30s")
	v.SetDefault("metrics.retention_days", 30)
	v.SetDefault("metrics.sources", []map[string]any{
		{"metric": "cpu_temp_c", "tool": "get_system_info", "args": map[string]any{"include": []any{"cpu"}}, "field": "cpu_temp_c", "label_ru": "Температура CPU", "label_en": "CPU temperature", "unit": "°C"},
		{"metric": "cpu_percent", "tool": "get_system_info", "args": map[string]any{"include": []any{"cpu"}}, "field": "cpu_percent", "aggregate": "avg", "label_ru": "Загрузка CPU", "label_en": "CPU load", "unit": "%"},
		{"metric": "mem_used_percent", "tool": "get_system_info", "args": map[string]any{"include": []any{"memory"}}, "field": "memory.used_percent", "label_ru": "Память", "label_en": "Memory", "unit": "%"},
		{"metric": "disk_used_percent", "tool": "get_system_info", "args": map[string]any{"include": []any{"disk"}}, "field": "disk.used_percent", "label_ru": "Диск", "label_en": "Disk", "unit": "%"},
		{"metric": "gps_speed_kmh", "tool": "get_gps", "field": "speed_kmh", "label_ru": "Скорость", "label_en": "Speed", "unit": "km/h"},
		{"metric": "fridge_c", "tool": "get_fridge_temp", "field": "fridge_c", "label_ru": "Холодильник", "label_en": "Fridge", "unit": "°C"},
		{"metric": "freezer_c", "tool": "get_fridge_temp", "field": "freezer_c", "label_ru": "Морозилка", "label_en": "Freezer", "unit": "°C"},
	})

	// MCP defaults
	v.SetDefault("mcp.server_name", "bosun")
	v.SetDefault("mcp.transport", "stdio")
	v.SetDefault("mcp.log_level", "info")

	// Web defaults bind only to loopback. LAN deployments should set an
	// explicit private address or 0.0.0.0 in config.yaml — see
	// webui.ValidateBind for what's accepted.
	v.SetDefault("web.bind", "127.0.0.1:8080")
	v.SetDefault("web.request_timeout", "600s")
	v.SetDefault("web.default_language", "ru")
	// Two budgets: the local fallback model has a tight context window, the
	// remote model's is effectively unlimited by comparison. Which one is
	// used to trim a given request is decided at request time from current
	// connectivity — see internal/webui/server.go. See docs/token-budget.md.
	v.SetDefault("web.history.local.turns", 4)
	v.SetDefault("web.history.local.max_chars", 4000)
	v.SetDefault("web.history.remote.turns", 40)
	v.SetDefault("web.history.remote.max_chars", 60000)
	v.SetDefault("web.session_ttl", "24h")
	v.SetDefault("web.max_sessions", 100)

	// Public, keyless online tools
	v.SetDefault("online_tools.duckduckgo_url", "https://html.duckduckgo.com/html/")
	v.SetDefault("online_tools.wikipedia_url", "https://{lang}.wikipedia.org/api/rest_v1/page/summary/{title}")
	v.SetDefault("online_tools.request_timeout", "10s")

	// Geocoding for get_directions (same free public services as weather)
	v.SetDefault("maps.geocoding_url", "https://geocoding-api.open-meteo.com/v1/search")
	v.SetDefault("maps.nominatim_url", "https://nominatim.openstreetmap.org/search")
	v.SetDefault("maps.timeout", "8s")

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
