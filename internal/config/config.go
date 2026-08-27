package config

import (
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
	Backup    BackupConfig    `mapstructure:"backup"`
	Alerts    AlertsConfig    `mapstructure:"alerts"`
	Sandbox   SandboxConfig   `mapstructure:"sandbox"`
	Adventure AdventureConfig `mapstructure:"adventure"`
	Cameras   []CameraConfig  `mapstructure:"cameras"`
	FileDump  FileDumpConfig  `mapstructure:"filedump"`
}

// AdventureConfig is the optional text-adventure game feature (see
// docs/adventure.md) — internal/adventure registers its tool only when
// Enabled is true. NarrateLocal/NarrateRemote apply only to game mode
// (internal/webui/adventure.go): whether that path's raw game output
// gets rephrased by one plain LLM call before reaching the user, per
// currently active provider. Both default false (raw text) so the game
// keeps working with zero LLM calls even fully offline — narration is
// a decoration, never a dependency. The opportunistic adventure_game
// LLM tool is a separate path that always lets the model narrate
// naturally, like any other tool; these two flags don't apply to it.
// MediaDir, if set, points GET /static/adventure/ at tools/artgen's
// generated output (a plain host directory, bind-mounted in — see
// docker-compose.yml) — the ~40MB of location art/audio is deliberately
// never committed to git or baked into the binary.
type AdventureConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	NarrateLocal  bool   `mapstructure:"narrate_local"`
	NarrateRemote bool   `mapstructure:"narrate_remote"`
	MediaDir      string `mapstructure:"media_dir"`
}

// FileDumpConfig is the optional raw-file storage tree feature (see
// docs/filedump.md) — internal/filedump.Store is constructed, and its
// routes registered, only when Path is non-empty; empty means the
// feature is off, same convention as AdventureConfig.MediaDir. Unlike
// DocumentsConfig.Path (which always resolves to a default when empty,
// since document search is always on), there's no default here — a raw
// browsable file tree is opt-in, not something to create unasked.
type FileDumpConfig struct {
	Path string `mapstructure:"path"`
}

// CameraConfig is one WiFi camera — see docs/cameras.md. Config-only, not
// settings-page-editable: which cameras exist is an infrastructure
// decision, not something to add/remove from a phone. A camera's own
// stream server accepts only one client, which is exactly why this
// exists at all — internal/cameras.Relay is the one thing that ever
// connects directly to StreamURL; everything else (the recorder, any
// number of live browser viewers) is a subscriber of the relay instead.
type CameraConfig struct {
	// Name is a URL-safe identifier used in API paths
	// (/api/cameras/<name>/...) and the recorded segments' directory.
	Name      string `mapstructure:"name"`
	LabelRU   string `mapstructure:"label_ru"`
	LabelEN   string `mapstructure:"label_en"`
	StreamURL string `mapstructure:"stream_url"`
	// Record turns on cyclic (ring-buffer) archival for this camera via
	// an ffmpeg subprocess reading from the relay (never the camera
	// directly) — see docs/cameras.md.
	Record bool `mapstructure:"record"`
	// SegmentSeconds/SegmentCount size the ring: SegmentCount segments of
	// SegmentSeconds each, oldest overwritten once full.
	SegmentSeconds int `mapstructure:"segment_seconds"`
	SegmentCount   int `mapstructure:"segment_count"`
}

// BackupConfig holds settings for the manual, on-demand `smarthelper
// backup` command (internal/backup) — there's no scheduled/automatic
// backup, deliberately: it only ever runs when explicitly invoked, so it
// never spends bandwidth the user didn't ask to spend right now.
type BackupConfig struct {
	// DataDir is the directory to archive — memos.json, documents.json,
	// sessions.json, settings.json, metric_merges.json, errors.jsonl,
	// document-images/, metrics.db (dumped to SQL, not copied raw). Empty
	// (the default) uses the same ~/.local/share/bosun every store already
	// defaults to on its own (see e.g. tools.NewMemoTool) — override only
	// if memo.path/documents.path/etc. were pointed somewhere nonstandard.
	DataDir string         `mapstructure:"data_dir"`
	S3      BackupS3Config `mapstructure:"s3"`
}

// BackupS3Config is the destination for `smarthelper backup` — any
// S3-compatible object store (AWS S3, Backblaze B2, MinIO, Wasabi, ...),
// path-style requests only (see internal/backup.S3Config). Credentials are
// env var *names* (same indirection as llm.remote.api_key_env), resolved
// at backup time, never written to config.yaml itself.
type BackupS3Config struct {
	Endpoint           string `mapstructure:"endpoint"`
	Region             string `mapstructure:"region"`
	Bucket             string `mapstructure:"bucket"`
	AccessKeyIDEnv     string `mapstructure:"access_key_id_env"`
	SecretAccessKeyEnv string `mapstructure:"secret_access_key_env"`
}

// AlertsConfig is "something is wrong enough a human should hear about it
// right now" (internal/alerts, docs/alerts.md): NOAA weather alerts for
// the current position, and any internal/metrics-sampled metric crossing
// a configured threshold — delivered through whichever channels below are
// both configured here and enabled on the settings page (which toggle is
// live, which is fixed at startup mirrors backup.s3/settings.BackupAutoEnabled).
type AlertsConfig struct {
	NOAA       AlertsNOAAConfig        `mapstructure:"noaa"`
	Thresholds []AlertsThresholdConfig `mapstructure:"thresholds"`
	Channels   AlertsChannelsConfig    `mapstructure:"channels"`
}

// AlertsNOAAConfig: either a fixed point (Latitude/Longitude), or UseGPS
// to check whatever position the get_gps tool reports right now on every
// tick instead — the point that actually matters for a vehicle that
// moves, unlike a fixed config value that's only ever right by luck.
// weather.gov has no coverage outside the US; a point outside it just
// returns no active alerts, not an error.
type AlertsNOAAConfig struct {
	Latitude      float64 `mapstructure:"latitude"`
	Longitude     float64 `mapstructure:"longitude"`
	UseGPS        bool    `mapstructure:"use_gps"`
	CheckInterval string  `mapstructure:"check_interval"`
}

// AlertsThresholdConfig is one configured limit — Metric must be a name
// internal/metrics.Store already has samples for (see metrics.sources);
// this has no idea what a metric physically represents, so a future
// battery-charge or tank-level sensor needs no code change here, only a
// new metrics.sources entry and a new threshold entry.
type AlertsThresholdConfig struct {
	Metric   string  `mapstructure:"metric"`
	Operator string  `mapstructure:"operator"` // ">", "<", ">=", "<=", "=="
	Value    float64 `mapstructure:"value"`
	Title    string  `mapstructure:"title"`
}

type AlertsChannelsConfig struct {
	Telegram AlertsTelegramConfig `mapstructure:"telegram"`
	Webhook  AlertsWebhookConfig  `mapstructure:"webhook"`
	Speaker  AlertsSpeakerConfig  `mapstructure:"speaker"`
}

// AlertsTelegramConfig: BotTokenEnv is an env var *name* (.env), the same
// indirection as llm.remote.api_key_env — never written into config.yaml
// itself.
type AlertsTelegramConfig struct {
	BotTokenEnv string `mapstructure:"bot_token_env"`
	ChatID      string `mapstructure:"chat_id"`
}

type AlertsWebhookConfig struct {
	URL string `mapstructure:"url"`
}

// AlertsSpeakerConfig: Enabled gates whether this channel is configured
// at all (the settings page's own toggle then decides whether it's
// actually used) — needs /dev/snd passed through to the container and
// aplay installed; see docs/alerts.md.
type AlertsSpeakerConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	PlayerPath string `mapstructure:"player_path"`
}

// SandboxConfig is the `run_code` tool (internal/tools/codeexec.go,
// docs/sandbox.md): lets the LLM write and run a short Python program for
// computation it's bad at itself (math, parsing, simulation). Off by
// default — enabling it needs BOTH Enabled here AND the separate
// `sandboxd` service actually running (Compose profile "sandbox", see
// docker-compose.yml) — this file alone never starts anything with
// /var/run/docker.sock access.
//
// One config file, shared read-only between the `bosun` container (which
// only reads URL/Enabled, to call sandboxd) and the `sandboxd` container
// (which reads everything else, to run itself) — the same file already
// mounted into both, no second config mechanism.
type SandboxConfig struct {
	// Enabled registers the run_code tool in bosun. Meaningless unless
	// sandboxd is also actually running (see above).
	Enabled bool `mapstructure:"enabled"`
	// URL is sandboxd's own address, as bosun's tool sees it.
	URL string `mapstructure:"url"`
	// ListenAddr is what sandboxd itself binds to — loopback only; nothing
	// but bosun, on the same host, needs to reach this.
	ListenAddr string `mapstructure:"listen_addr"`
	// ScratchDir/StateDir are sandboxd-side paths: per-session workspace
	// bind mounts, and the reaper's persisted session-state JSON
	// (internal/sandbox, same atomicWriteJSON pattern as
	// internal/backup/schedule.go), respectively.
	ScratchDir string `mapstructure:"scratch_dir"`
	StateDir   string `mapstructure:"state_dir"`
	// SessionTTL: how long an idle session's workspace container survives
	// before the reaper removes it — a duration string like
	// alerts.noaa.check_interval.
	SessionTTL string `mapstructure:"session_ttl"`
	// TimeoutSeconds bounds a single execution's wall-clock time —
	// reliability (don't let a runaway script hang sandboxd), not security.
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
	// MemoryLimit/CPULimit are `docker run --memory`/`--cpus` values —
	// also reliability, not security: this box runs LLM inference too and
	// a memory-bomb/infinite-loop script from the weak local model
	// shouldn't be able to take the whole thing down.
	MemoryLimit string `mapstructure:"memory_limit"`
	CPULimit    string `mapstructure:"cpu_limit"`
	// RuntimeImage is the locally built (not pulled at request time) image
	// each session's container runs — see deploy/sandbox-runtime/Dockerfile.
	RuntimeImage string `mapstructure:"runtime_image"`
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
	// AttachImageMinRelevance is the threshold documents.Store.AttachOrphanedImages
	// uses, separate from (and higher than) MinSearchRelevance: showing a
	// so-so search result is low-stakes, but permanently merging an image
	// onto the wrong text chunk across two unrelated documents is a real
	// mistake a mere "somewhat relevant" score isn't a safe bar for —
	// confirmed live: an image from an unrelated Ford manual got merged
	// onto an unrelated Valvoline product sheet's chunk at 0.41. See
	// docs/memo-search.md.
	AttachImageMinRelevance float64 `mapstructure:"attach_image_min_relevance"`
	// MetricMergeCheckInterval is how often the background pass checks
	// known_metrics (see internal/tools/memo.go's maintenance) for pairs
	// that might be the same physical counter under two different names —
	// e.g. "odometer_miles" and "oil_change_odometer" both tracking a car's
	// mileage. It only ever proposes a merge for a human to approve or
	// reject in the web UI; nothing is renamed automatically. Defaults to
	// 24h — metric names change far less often than tags. See
	// docs/maintenance-tracking.md.
	MetricMergeCheckInterval string `mapstructure:"metric_merge_check_interval"`
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
// WAV — see deploy/piper/wav-pcm16.patch) and a voice model. BinaryPath,
// ModelPath, and EspeakDataPath are required together; ModelPath empty
// disables the feature. EnglishModelPath is optional — set it to a
// second Piper voice (e.g. en_US-ryan-medium.onnx) and text with no
// Cyrillic characters is read with that voice instead of ModelPath's,
// so a Russian voice never has to read English text (the adventure
// game's output, for one, is always English regardless of UI
// language). Left empty, every request uses ModelPath, exactly as
// before this field existed.
type TTSConfig struct {
	BinaryPath       string `mapstructure:"binary_path"`
	ModelPath        string `mapstructure:"model_path"`
	EnglishModelPath string `mapstructure:"english_model_path"`
	EspeakDataPath   string `mapstructure:"espeak_data_path"`
}

// STTConfig points at a running `whisper-server` (see
// deploy/whisper/Dockerfile) — a separate long-running process, unlike
// TTS's per-request subprocess, since whisper.cpp's model load time is
// significant enough to be worth keeping resident. BaseURL empty (the
// default) disables /api/stt entirely.
// STTConfig points at a whisper.cpp-compatible transcription endpoint.
// APIKeyEnv empty (the default) means BaseURL is a local whisper-server
// instance (internal/voice.WhisperCppSTT, no auth, no Model field).
// APIKeyEnv set switches to internal/voice.RemoteSTT instead — an
// OpenAI-compatible /audio/transcriptions endpoint (e.g. this
// deployment's own reverse proxy fronting Groq's hosted Whisper API),
// which needs both authentication and a Model name since a remote
// endpoint can serve more than one.
type STTConfig struct {
	BaseURL   string `mapstructure:"base_url"`
	Language  string `mapstructure:"language"`
	Model     string `mapstructure:"model"`
	APIKeyEnv string `mapstructure:"api_key_env"`
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
	v.SetDefault("memo.attach_image_min_relevance", 0.6)
	v.SetDefault("memo.metric_merge_check_interval", "24h")

	// Metrics defaults — see docs/monitoring.md. This default source list
	// covers every sensor this project ships with today; a deployment
	// with more hardware (battery, water tank, ...) appends to it in
	// config.yaml rather than replacing it, unless it explicitly wants to
	// drop one of these.
	v.SetDefault("metrics.enabled", true)
	v.SetDefault("metrics.interval", "30s")
	v.SetDefault("metrics.retention_days", 30)
	v.SetDefault("backup.s3.access_key_id_env", "BACKUP_S3_ACCESS_KEY_ID")
	v.SetDefault("backup.s3.secret_access_key_env", "BACKUP_S3_SECRET_ACCESS_KEY")

	v.SetDefault("alerts.noaa.check_interval", "15m")
	v.SetDefault("alerts.channels.telegram.bot_token_env", "ALERTS_TELEGRAM_BOT_TOKEN")
	v.SetDefault("alerts.channels.speaker.player_path", "aplay")

	v.SetDefault("sandbox.url", "http://127.0.0.1:8090")
	v.SetDefault("sandbox.listen_addr", "127.0.0.1:8090")
	v.SetDefault("sandbox.scratch_dir", "/data/sandbox/workspaces")
	v.SetDefault("sandbox.state_dir", "/data/sandbox/state")
	v.SetDefault("sandbox.session_ttl", "15m")
	v.SetDefault("sandbox.timeout_seconds", 30)
	v.SetDefault("sandbox.memory_limit", "512m")
	v.SetDefault("sandbox.cpu_limit", "1")
	v.SetDefault("sandbox.runtime_image", "bosun-sandbox-python:local")

	v.SetDefault("adventure.enabled", false)
	v.SetDefault("adventure.narrate_local", false)
	v.SetDefault("adventure.narrate_remote", false)

	v.SetDefault("metrics.sources", []map[string]any{
		{"metric": "cpu_temp_c", "tool": "get_system_info", "args": map[string]any{"include": []any{"cpu"}}, "field": "cpu.temp_c", "label_ru": "Температура CPU", "label_en": "CPU temperature", "unit": "°C"},
		{"metric": "cpu_percent", "tool": "get_system_info", "args": map[string]any{"include": []any{"cpu"}}, "field": "cpu.used_percent", "label_ru": "Загрузка CPU", "label_en": "CPU load", "unit": "%"},
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
