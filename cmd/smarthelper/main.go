// Command smarthelper is the entry point for bosun-smarthelper.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/roman220/bosun-smarthelper/internal/agent"
	"github.com/roman220/bosun-smarthelper/internal/alerts"
	"github.com/roman220/bosun-smarthelper/internal/backup"
	"github.com/roman220/bosun-smarthelper/internal/cameras"
	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/embeddings"
	"github.com/roman220/bosun-smarthelper/internal/errlog"
	"github.com/roman220/bosun-smarthelper/internal/llm"
	"github.com/roman220/bosun-smarthelper/internal/mcp"
	"github.com/roman220/bosun-smarthelper/internal/metrics"
	"github.com/roman220/bosun-smarthelper/internal/sandbox"
	"github.com/roman220/bosun-smarthelper/internal/settings"
	"github.com/roman220/bosun-smarthelper/internal/tools"
	"github.com/roman220/bosun-smarthelper/internal/voice"
	"github.com/roman220/bosun-smarthelper/internal/webui"
)

const version = "0.1.0"

func main() {
	_ = godotenv.Load() // optional .env; missing file is not an error
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:   "smarthelper",
		Short: "Bosun (Starpom), a local-first assistant with hybrid LLM routing and MCP tools",
	}
	root.AddCommand(versionCmd(), mcpCmd(), chatCmd(), serveCmd(), errorsCmd(), documentsCmd(), backupCmd(), restoreCmd(), sandboxServeCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Bosun (Starpom) " + version)
		},
	}
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server over stdio, exposing sensor tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			registry, _ := buildRegistry(cfg)

			server := mcp.NewServer(cfg.MCP.ServerName, version, registry, logger)
			server.SetErrorLog(openErrorLog(cfg, logger))
			logger.Info("starting MCP server", "transport", cfg.MCP.Transport, "tools", registry.List())

			return server.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
}

// openErrorLog opens the shared tool/LLM failure log. A failure to open it
// (e.g. permissions) is logged and treated as "logging disabled" rather
// than a fatal error — the assistant should keep working either way.
func openErrorLog(cfg *config.Config, logger *slog.Logger) *errlog.Logger {
	errLog, err := errlog.Open(cfg.ErrorLog.Path)
	if err != nil {
		logger.Warn("could not open error log; failures will not be recorded", "error", err)
		return nil
	}
	return errLog
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the LAN-only web interface",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := webui.ValidateBind(cfg.Web.Bind); err != nil {
				return err
			}
			if cfg.Web.HTTPFallbackBind != "" {
				if err := webui.ValidateBind(cfg.Web.HTTPFallbackBind); err != nil {
					return fmt.Errorf("http fallback bind: %w", err)
				}
			}

			requestTimeout, err := time.ParseDuration(cfg.Web.RequestTimeout)
			if err != nil || requestTimeout <= 0 {
				return fmt.Errorf("invalid web request timeout %q", cfg.Web.RequestTimeout)
			}
			sessionTTL, err := time.ParseDuration(cfg.Web.SessionTTL)
			if err != nil || sessionTTL <= 0 {
				return fmt.Errorf("invalid web session TTL %q", cfg.Web.SessionTTL)
			}

			router, err := llm.NewRouter(&cfg.LLM)
			if err != nil {
				return fmt.Errorf("create LLM router: %w", err)
			}
			router.CheckConnectivity(cmd.Context())

			// The settings store (docs/settings.md) is seeded from config.yaml's
			// current values the first time it's created; once its file exists,
			// it — not config.yaml — is the source of truth for these fields, so
			// a value saved from the settings page survives a restart.
			settingsPath := cfg.Web.SettingsStorePath
			if settingsPath == "" {
				settingsPath = settings.DefaultPath()
			}
			settingsStore, err := settings.Load(settingsPath, settings.Data{
				NameRU:            cfg.Assistant.NameRU,
				NameEN:            cfg.Assistant.NameEN,
				StylePrompt:       cfg.Assistant.StylePrompt,
				DefaultLanguage:   cfg.Web.DefaultLanguage,
				RemoteTemperature: cfg.LLM.Remote.Temperature,
				LocalTemperature:  cfg.LLM.Local.Temperature,
				CanonicalTags:     cfg.Memo.CanonicalTags,
				AlertsThresholds:  seedThresholdRules(cfg.Alerts.Thresholds),
			})
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}
			live := settingsStore.Get()
			router.SetTemperatures(live.RemoteTemperature, live.LocalTemperature)

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			registry, docStore := buildRegistry(cfg)
			ag := agent.New(router, registry, router.NetworkAvailable)
			ag.SetPersona(live.NameRU, live.NameEN, live.StylePrompt)
			ag.SetErrorLog(openErrorLog(cfg, logger))

			storePath := cfg.Web.SessionStorePath
			if storePath == "" {
				storePath = webui.DefaultSessionStorePath()
			}
			server := webui.NewServer(ag, func() webui.Status {
				online := router.NetworkAvailable(context.Background())
				return webui.Status{
					Online:         online,
					Provider:       router.ActiveProvider(),
					AvailableTools: registry.AvailableList(online),
				}
			}, requestTimeout, live.DefaultLanguage, logger, webui.SessionOptions{
				Local:       webui.HistoryBudget{Turns: cfg.Web.History.Local.Turns, MaxChars: cfg.Web.History.Local.MaxChars},
				Remote:      webui.HistoryBudget{Turns: cfg.Web.History.Remote.Turns, MaxChars: cfg.Web.History.Remote.MaxChars},
				TTL:         sessionTTL,
				MaxSessions: cfg.Web.MaxSessions,
				StorePath:   storePath,
			})
			server.SetDocumentStore(docStore)
			server.SetToolRegistry(registry)
			server.SetSettingsStore(settingsStore)
			server.SetTemperatureController(router)
			server.SetProviderOverrideController(router)
			server.SetCACertFile(cfg.Web.CACertFile)
			var ttsEngine voice.TTSEngine
			if cfg.Voice.TTS.ModelPath != "" {
				ttsEngine = &voice.PiperTTS{
					BinaryPath:     cfg.Voice.TTS.BinaryPath,
					ModelPath:      cfg.Voice.TTS.ModelPath,
					EspeakDataPath: cfg.Voice.TTS.EspeakDataPath,
				}
				server.SetTTSEngine(ttsEngine)
			}
			if cfg.Voice.STT.BaseURL != "" {
				server.SetSTTEngine(&voice.WhisperCppSTT{
					BaseURL:  cfg.Voice.STT.BaseURL,
					Language: cfg.Voice.STT.Language,
				})
			}

			if cfg.Metrics.Enabled {
				metricsStore, err := metrics.Open(cfg.Metrics.StorePath)
				if err != nil {
					logger.Warn("could not open metrics store; monitoring dashboard disabled", "error", err)
				} else {
					server.SetMetricsStore(metricsStore)
					labels := make(map[string]webui.MetricLabel, len(cfg.Metrics.Sources))
					for _, source := range cfg.Metrics.Sources {
						labels[source.Metric] = webui.MetricLabel{RU: source.LabelRU, EN: source.LabelEN, Unit: source.Unit}
					}
					server.SetMetricsLabels(labels)
					interval, err := time.ParseDuration(cfg.Metrics.Interval)
					if err != nil || interval <= 0 {
						interval = 30 * time.Second
					}
					retentionDays := cfg.Metrics.RetentionDays
					if retentionDays <= 0 {
						retentionDays = 30
					}
					collector := metrics.NewCollector(metricsStore, registry, cfg.Metrics.Sources, logger)
					go collector.Run(cmd.Context(), interval, time.Duration(retentionDays)*24*time.Hour)

					// Always started, not gated on config.yaml having any
					// alerts.thresholds entries — rules are web-managed
					// now (settings.Data.AlertsThresholds, see
					// runThresholdChecker) and can be added with zero
					// config.yaml changes; the checker itself is a no-op
					// on every tick until at least one rule exists.
					if alertsDataDir, err := resolveDataDir(""); err != nil {
						logger.Warn("could not resolve data directory; threshold alerts disabled", "error", err)
					} else {
						go runThresholdChecker(cmd.Context(), cfg, settingsStore, metricsStore, ttsEngine, alertsDataDir, logger)
					}
				}
			}

			if memoTool, ok := registry.Get("memo"); ok {
				if mt, ok := memoTool.(*tools.MemoTool); ok {
					server.SetMemoTool(mt)

					interval, err := time.ParseDuration(cfg.Memo.TagNormalizeInterval)
					if err != nil || interval <= 0 {
						interval = 5 * time.Minute
					}
					go runTagNormalizer(cmd.Context(), server, mt, router, settingsStore, interval, logger)

					mergeInterval, err := time.ParseDuration(cfg.Memo.MetricMergeCheckInterval)
					if err != nil || mergeInterval <= 0 {
						mergeInterval = 24 * time.Hour
					}
					go runMetricMergeChecker(cmd.Context(), server, mt, router, mergeInterval, logger)
				}
			}

			if s3cfg, err := resolveBackupS3(cfg); err != nil {
				logger.Info("backup not configured", "reason", err)
			} else if dataDir, err := resolveDataDir(cfg.Backup.DataDir); err != nil {
				logger.Warn("could not resolve backup data directory; backup disabled", "error", err)
			} else {
				server.SetBackupConfig(&s3cfg, dataDir)
				go runBackupScheduler(cmd.Context(), server, settingsStore, s3cfg, dataDir, logger)
			}

			server.SetAlertsConfigured(
				cfg.Alerts.Channels.Telegram.ChatID != "" && os.Getenv(cfg.Alerts.Channels.Telegram.BotTokenEnv) != "",
				cfg.Alerts.Channels.Webhook.URL != "",
				cfg.Alerts.Channels.Speaker.Enabled,
			)
			server.SetAlertsTestSender(func(ctx context.Context, channel string) error {
				return sendTestAlert(ctx, cfg, ttsEngine, settingsStore.Get().DefaultLanguage, logger, channel)
			})

			noaaCfg := cfg.Alerts.NOAA
			if noaaCfg.UseGPS || noaaCfg.Latitude != 0 || noaaCfg.Longitude != 0 {
				if alertsDataDir, err := resolveDataDir(""); err != nil {
					logger.Warn("could not resolve data directory; NOAA alerts disabled", "error", err)
				} else {
					go runNOAAChecker(cmd.Context(), cfg, registry, settingsStore, ttsEngine, alertsDataDir, logger)
				}
			}

			if len(cfg.Cameras) > 0 {
				cameraConfigs := make([]cameras.Config, 0, len(cfg.Cameras))
				for _, c := range cfg.Cameras {
					cameraConfigs = append(cameraConfigs, cameras.Config{Name: c.Name, LabelRU: c.LabelRU, LabelEN: c.LabelEN, StreamURL: c.StreamURL})
				}
				cameraManager := cameras.NewManager(cameraConfigs, logger)
				cameraManager.Start(cmd.Context())
				if cameraDataDir, err := resolveCameraDataDir(); err != nil {
					logger.Warn("could not resolve camera data directory; archive browsing/recording disabled", "error", err)
				} else {
					server.SetCameraManager(cameraManager, cameraDataDir)
					recorderBaseURL := internalRecorderBaseURL(cfg)
					for _, c := range cfg.Cameras {
						if !c.Record {
							continue
						}
						if recorderBaseURL == "" {
							logger.Warn("camera recording requested but no plain-HTTP address is available for the recorder to reach the relay (TLS-only with no web.http_fallback_bind)", "camera", c.Name)
							continue
						}
						go runCameraRecorder(cmd.Context(), c, recorderBaseURL, cameraDataDir, logger)
					}
				}
			}

			scheme := "http"
			if cfg.Web.TLSCertFile != "" && cfg.Web.TLSKeyFile != "" {
				scheme = "https"
			}
			logger.Info("starting web interface", "address", cfg.Web.Bind, "scheme", scheme)
			if scheme == "https" && cfg.Web.HTTPFallbackBind != "" {
				logger.Info("also serving plain HTTP fallback", "address", cfg.Web.HTTPFallbackBind)
			}
			return server.Serve(cmd.Context(), cfg.Web.Bind, cfg.Web.TLSCertFile, cfg.Web.TLSKeyFile, cfg.Web.HTTPFallbackBind)
		},
	}
}

func chatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat [message]",
		Short: "Ask the assistant a one-off question, using tools as needed",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			router, err := llm.NewRouter(&cfg.LLM)
			if err != nil {
				return fmt.Errorf("create LLM router: %w", err)
			}
			// Refresh connectivity before routing: configurable remote endpoint
			// when online, falling back to the local model when offline.
			router.CheckConnectivity(cmd.Context())

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			registry, _ := buildRegistry(cfg)
			ag := agent.New(router, registry, router.NetworkAvailable)
			ag.SetPersona(cfg.Assistant.NameRU, cfg.Assistant.NameEN, cfg.Assistant.StylePrompt)
			ag.SetErrorLog(openErrorLog(cfg, logger))

			answer, err := ag.Ask(cmd.Context(), strings.Join(args, " "))
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			fmt.Println(answer)
			return nil
		},
	}
}

func errorsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "errors",
		Short: "Show recent tool/LLM failures recorded for review",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			entries, err := errlog.Tail(cfg.ErrorLog.Path, limit)
			if err != nil {
				return fmt.Errorf("read error log: %w", err)
			}
			if len(entries) == 0 {
				fmt.Println("No recorded failures.")
				return nil
			}

			for _, entry := range entries {
				fmt.Printf("%s [%s] %s: %s\n", entry.Time.Format(time.RFC3339), entry.Category, entry.Detail, entry.Error)
			}
			fmt.Println("\nBy category/detail:")
			for _, summary := range errlog.Summarize(entries) {
				fmt.Printf("  %-12s %-24s x%d\n", summary.Category, summary.Detail, summary.Count)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum recent entries to show (0 = all)")
	return cmd
}

// documentsCmd holds maintenance operations on the document store that
// don't belong behind a chat tool call (see docs/memo-search.md on why
// document upload itself is web-UI-only).
func documentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "documents",
		Short: "Maintenance operations on uploaded reference documents",
	}
	cmd.AddCommand(attachImagesCmd())
	return cmd
}

// backupCmd builds a tar.gz snapshot of the persistent data directory
// (memos, documents, sessions, settings, the error log, and metrics —
// dumped to SQL rather than copied raw, see internal/backup.DumpSQL) and
// uploads it to an S3-compatible bucket (config.yaml's backup.s3). There
// is deliberately no scheduled/automatic variant: this only ever runs
// when a human types it, so it never spends bandwidth uninvited — see
// docs/backup.md.
// resolveBackupS3 turns config.yaml's backup.s3 section plus the env vars
// it names into a ready-to-use backup.S3Config, shared by backupCmd and
// restoreCmd so the same validation/error messages apply to both.
func resolveBackupS3(cfg *config.Config) (backup.S3Config, error) {
	s3cfg := cfg.Backup.S3
	if s3cfg.Endpoint == "" || s3cfg.Bucket == "" {
		return backup.S3Config{}, fmt.Errorf("backup.s3.endpoint and backup.s3.bucket must be set in config.yaml")
	}
	accessKeyID := os.Getenv(s3cfg.AccessKeyIDEnv)
	secretAccessKey := os.Getenv(s3cfg.SecretAccessKeyEnv)
	if accessKeyID == "" || secretAccessKey == "" {
		return backup.S3Config{}, fmt.Errorf("%s and %s must be set (in .env)", s3cfg.AccessKeyIDEnv, s3cfg.SecretAccessKeyEnv)
	}
	return backup.S3Config{
		Endpoint:        s3cfg.Endpoint,
		Region:          s3cfg.Region,
		Bucket:          s3cfg.Bucket,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	}, nil
}

// resolveDataDir mirrors every store's own default (see e.g.
// tools.NewMemoTool) so backup/restore agree with the running service on
// where its data actually lives unless backup.data_dir overrides it.
func resolveDataDir(configuredDir string) (string, error) {
	if configuredDir != "" {
		return configuredDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "bosun"), nil
}

// resolveCameraDataDir is a sibling of resolveDataDir's own default
// (~/.local/share/bosun), not inside it — camera archives
// (internal/cameras, docs/cameras.md) must stay outside
// cfg.Backup.DataDir so they're excluded from the S3 backup by
// construction, the same reasoning docs/dashcam.md documented for the
// standalone service this replaces.
func resolveCameraDataDir() (string, error) {
	bosunDataDir, err := resolveDataDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(bosunDataDir), "dashcam"), nil
}

// internalRecorderBaseURL is the address a camera recorder's ffmpeg
// subprocess uses to reach its own camera's relay endpoint
// (/api/cameras/<name>/stream) — never the camera directly, so the
// camera's single client slot stays reserved for the relay itself (see
// docs/cameras.md). Always plain HTTP, even when the web UI serves TLS
// externally: this is a loopback-only call, and giving ffmpeg a
// self-signed/Let's-Encrypt cert to validate would be needless
// complexity for a connection that never leaves the host. Returns "" if
// TLS is configured with no plain-HTTP fallback bind at all, meaning
// there's genuinely no way to reach the relay without a cert — recording
// simply can't work with that configuration.
func internalRecorderBaseURL(cfg *config.Config) string {
	addr := cfg.Web.Bind
	if cfg.Web.TLSCertFile != "" && cfg.Web.TLSKeyFile != "" {
		if cfg.Web.HTTPFallbackBind == "" {
			return ""
		}
		addr = cfg.Web.HTTPFallbackBind
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return "http://127.0.0.1:" + port
}

// runCameraRecorder cyclically records one camera's relay stream (never
// the camera directly — see internalRecorderBaseURL) via the same
// ffmpeg segment-wrap invocation proven live in the standalone `dashcam`
// service this replaces (docs/cameras.md). If ffmpeg exits for any
// reason (the relay restarting, a transient hiccup), this waits a few
// seconds and starts it again, until ctx is cancelled.
func runCameraRecorder(ctx context.Context, cam config.CameraConfig, baseURL, dataDir string, logger *slog.Logger) {
	segmentSeconds := cam.SegmentSeconds
	if segmentSeconds <= 0 {
		segmentSeconds = 300
	}
	segmentCount := cam.SegmentCount
	if segmentCount <= 0 {
		segmentCount = 50
	}
	camDir := filepath.Join(dataDir, cam.Name)
	if err := os.MkdirAll(camDir, 0o755); err != nil {
		logger.Error("create camera archive directory", "camera", cam.Name, "error", err)
		return
	}

	args := []string{
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "2",
		"-i", fmt.Sprintf("%s/api/cameras/%s/stream", baseURL, cam.Name),
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-f", "segment", "-segment_time", strconv.Itoa(segmentSeconds),
		"-segment_wrap", strconv.Itoa(segmentCount), "-reset_timestamps", "1",
		filepath.Join(camDir, "cam_%03d.mp4"),
	}

	const restartDelay = 5 * time.Second
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil && ctx.Err() == nil {
			logger.Warn("camera recorder exited, restarting", "camera", cam.Name, "error", err, "stderr", stderr.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(restartDelay):
		}
	}
}

// sandboxServeCmd runs sandboxd — the only process in this stack that
// touches /var/run/docker.sock, deliberately separate from `serve`'s much
// larger, network-facing bosun process. See docs/sandbox.md for why, and
// internal/sandbox for the actual HTTP handler + reaper.
func sandboxServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "sandbox-serve",
		Short:  "Run the run_code tool's isolated execution service (internal use, see docs/sandbox.md)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

			tracker, err := sandbox.NewSessionTracker(cfg.Sandbox.StateDir)
			if err != nil {
				return fmt.Errorf("load sandbox session state: %w", err)
			}

			sessionTTL, err := time.ParseDuration(cfg.Sandbox.SessionTTL)
			if err != nil || sessionTTL <= 0 {
				sessionTTL = 15 * time.Minute
			}
			timeout := time.Duration(cfg.Sandbox.TimeoutSeconds) * time.Second
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			const maxTimeout = 120 * time.Second

			server := sandbox.NewServer(tracker, cfg.Sandbox.ScratchDir, cfg.Sandbox.RuntimeImage,
				cfg.Sandbox.MemoryLimit, cfg.Sandbox.CPULimit, timeout, maxTimeout, logger)

			if err := sandbox.Reconcile(cmd.Context(), server.Runner, tracker); err != nil {
				logger.Warn("reconcile sandbox session state against running containers", "error", err)
			}
			go sandbox.Run(cmd.Context(), server, 2*time.Minute, sessionTTL, logger)

			logger.Info("sandboxd listening", "addr", cfg.Sandbox.ListenAddr)
			return http.ListenAndServe(cfg.Sandbox.ListenAddr, server.Handler())
		},
	}
}

func backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Archive persistent data and upload it to an S3-compatible bucket",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			s3cfg, err := resolveBackupS3(cfg)
			if err != nil {
				return err
			}
			dataDir, err := resolveDataDir(cfg.Backup.DataDir)
			if err != nil {
				return err
			}

			var archive bytes.Buffer
			if err := backup.BuildArchive(&archive, dataDir); err != nil {
				return fmt.Errorf("build archive: %w", err)
			}

			key := fmt.Sprintf("bosun-backup-%s.tar.gz", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
			uploadCtx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			if err := backup.PutObject(uploadCtx, s3cfg, key, archive.Bytes(), "application/gzip"); err != nil {
				return fmt.Errorf("upload: %w", err)
			}
			// Any successful backup — CLI, web UI "back up now", or the
			// automatic schedule — resets the schedule's countdown the
			// same way, so they never fight over when the next automatic
			// run is actually due.
			if err := backup.RecordRun(dataDir, time.Now()); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not record backup schedule state: %v\n", err)
			}

			fmt.Printf("Uploaded %s (%.1f MB) to %s/%s\n", key, float64(archive.Len())/1e6, s3cfg.Bucket, key)
			return nil
		},
	}
	return cmd
}

// restoreCmd downloads a backup (the most recent one by default) and
// extracts it into --to, which defaults to a fresh, clearly-named
// directory rather than the live data directory — an in-place restore
// over real data is something to opt into explicitly (--to
// ~/.local/share/bosun or wherever backup.data_dir points), not something
// this command risks doing by accident.
func restoreCmd() *cobra.Command {
	var key, to string
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Download a backup from the S3-compatible bucket and extract it",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			s3cfg, err := resolveBackupS3(cfg)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			if key == "" {
				objects, err := backup.ListObjects(ctx, s3cfg, "bosun-backup-")
				if err != nil {
					return fmt.Errorf("list backups: %w", err)
				}
				if len(objects) == 0 {
					return fmt.Errorf("no backups found in %s", s3cfg.Bucket)
				}
				latest := objects[0]
				for _, o := range objects[1:] {
					if o.LastModified.After(latest.LastModified) {
						latest = o
					}
				}
				key = latest.Key
				fmt.Printf("No --key given; using the most recent backup: %s (%s)\n", key, latest.LastModified.Format(time.RFC3339))
			}

			if to == "" {
				to = fmt.Sprintf("./bosun-restore-%s", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
			}

			body, err := backup.GetObject(ctx, s3cfg, key)
			if err != nil {
				return fmt.Errorf("download %s: %w", key, err)
			}
			if err := backup.ExtractArchive(bytes.NewReader(body), to); err != nil {
				return fmt.Errorf("extract archive: %w", err)
			}

			fmt.Printf("Restored %s into %s\n", key, to)
			fmt.Println("Review it, then move/copy its contents into your real data directory")
			fmt.Println("(config.yaml's backup.data_dir, or ~/.local/share/bosun by default) when ready.")
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "Backup object key to restore (default: the most recent one)")
	cmd.Flags().StringVar(&to, "to", "", "Directory to extract into (default: a new ./bosun-restore-<timestamp> directory)")
	return cmd
}

func attachImagesCmd() *cobra.Command {
	var minRelevance float64
	cmd := &cobra.Command{
		Use:   "attach-images",
		Short: "Merge orphaned image chunks onto their best-matching text chunk (see documents.AttachOrphanedImages)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if minRelevance <= 0 {
				minRelevance = cfg.Memo.AttachImageMinRelevance
			}
			store := documents.NewStore(cfg.Documents.Path, embeddings.NewClient(&cfg.LLM.Embeddings))
			summary, err := store.AttachOrphanedImages(cmd.Context(), minRelevance)
			if err != nil {
				return fmt.Errorf("attach images: %w", err)
			}
			fmt.Printf("attached %d image chunks to a matching text chunk, %d left standalone (no match >= %.2f), %d now-empty documents removed\n",
				summary.Attached, summary.Unmatched, minRelevance, summary.EmptyDocumentsRemoved)
			return nil
		},
	}
	cmd.Flags().Float64Var(&minRelevance, "min-relevance", 0, "Minimum cosine similarity to merge (0 = use memo.attach_image_min_relevance from config)")
	return cmd
}

// runTagNormalizer periodically maps memos' free-form tags onto
// cfg.Memo.CanonicalTags (see internal/tools/memo_tags.go), but only when
// server.TryIdleAfter reports no chat request is in flight and none has
// finished in the last interval either — a busy assistant never falls
// behind because of this, and a user typing a follow-up right after a
// reply never queues behind background maintenance. Stops when ctx is
// cancelled (process shutdown).
// runTagNormalizer always runs (there's no separate on/off switch at
// startup) but is a no-op each tick when settingsStore.Get().CanonicalTags
// is currently empty — so turning the feature on later from the settings
// page (docs/settings.md) takes effect without a restart, at the cost of
// one cheap check per idle tick when it's off.
func runTagNormalizer(
	ctx context.Context,
	server *webui.Server,
	memoTool *tools.MemoTool,
	client *llm.Router,
	settingsStore *settings.Store,
	interval time.Duration,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			canonicalTags := settingsStore.Get().CanonicalTags
			if len(canonicalTags) == 0 {
				continue
			}
			server.TryIdleAfter(interval, func() {
				normCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				updated, err := memoTool.NormalizeTags(normCtx, client, canonicalTags, 10)
				if err != nil {
					logger.Warn("memo tag normalization failed", "error", err)
				} else if updated > 0 {
					logger.Info("normalized memo tags", "count", updated)
				}
			})
		}
	}
}

// runMetricMergeChecker periodically looks for known_metrics pairs (see
// internal/tools/memo_metric_merge.go's CheckMetricMerges) that might be
// the same physical counter under two different spellings, same idle-tick
// discipline as runTagNormalizer. It only ever proposes a merge for a
// human to approve or reject via the web UI's approval queue — nothing is
// renamed on its own. Stops when ctx is cancelled (process shutdown).
func runMetricMergeChecker(
	ctx context.Context,
	server *webui.Server,
	memoTool *tools.MemoTool,
	client *llm.Router,
	interval time.Duration,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			server.TryIdleAfter(interval, func() {
				checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				proposed, err := memoTool.CheckMetricMerges(checkCtx, client, 10)
				if err != nil {
					logger.Warn("metric merge check failed", "error", err)
				} else if proposed > 0 {
					logger.Info("proposed metric merges", "count", proposed)
				}
			})
		}
	}
}

// runBackupScheduler runs the same archive+upload logic as `smarthelper
// backup`/the web UI's "back up now" button, but only when the settings
// page's auto-backup toggle (internal/settings.Data.BackupAutoEnabled) is
// on — off by default, same as every other opt-in background pass in
// this project, and independent of whether backup.s3 is configured at
// all (checked once at startup by the caller). Ticks every 15 minutes to
// check whether a run is actually due (internal/backup.DueForRun) rather
// than sleeping for the full configured interval, so flipping the
// setting on mid-wait doesn't mean waiting out a stale timer.
func runBackupScheduler(
	ctx context.Context,
	server *webui.Server,
	settingsStore *settings.Store,
	s3cfg backup.S3Config,
	dataDir string,
	logger *slog.Logger,
) {
	const checkInterval = 15 * time.Minute
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data := settingsStore.Get()
			if !data.BackupAutoEnabled || data.BackupIntervalHours <= 0 {
				continue
			}
			due, err := backup.DueForRun(dataDir, data.BackupIntervalHours, time.Now())
			if err != nil {
				logger.Warn("check backup schedule", "error", err)
				continue
			}
			if !due {
				continue
			}
			server.TryIdleAfter(checkInterval, func() {
				runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				var archive bytes.Buffer
				if err := backup.BuildArchive(&archive, dataDir); err != nil {
					logger.Error("build scheduled backup archive", "error", err)
					return
				}
				key := fmt.Sprintf("bosun-backup-%s.tar.gz", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
				if err := backup.PutObject(runCtx, s3cfg, key, archive.Bytes(), "application/gzip"); err != nil {
					logger.Error("upload scheduled backup", "error", err)
					return
				}
				if err := backup.RecordRun(dataDir, time.Now()); err != nil {
					logger.Warn("record scheduled backup run", "error", err)
				}
				logger.Info("scheduled backup uploaded", "key", key, "size_bytes", archive.Len())
			})
		}
	}
}

// telegramNotifier/webhookNotifier/speakerNotifier each build one channel
// if it's both configured (config.yaml/.env) and enabled by the caller —
// shared by noaaAlertNotifiers (one global enabled flag per channel) and
// thresholdRuleNotifiers (one enabled flag per rule, per channel). Return
// a bare `nil` (not a typed nil pointer) on every "not applicable" path,
// so the caller's `!= nil` check against the alerts.Notifier interface
// behaves correctly.
func telegramNotifier(cfg config.AlertsTelegramConfig, enabled bool, logger *slog.Logger) alerts.Notifier {
	if cfg.ChatID == "" || !enabled {
		return nil
	}
	botToken := os.Getenv(cfg.BotTokenEnv)
	if botToken == "" {
		logger.Warn("telegram alerts enabled but bot token env var is empty", "env", cfg.BotTokenEnv)
		return nil
	}
	return &alerts.TelegramNotifier{BotToken: botToken, ChatID: cfg.ChatID}
}

func webhookNotifier(cfg config.AlertsWebhookConfig, enabled bool) alerts.Notifier {
	if cfg.URL == "" || !enabled {
		return nil
	}
	return &alerts.WebhookNotifier{URL: cfg.URL}
}

func speakerNotifier(cfg config.AlertsSpeakerConfig, enabled bool, ttsEngine voice.TTSEngine, language string, logger *slog.Logger) alerts.Notifier {
	if !cfg.Enabled || !enabled {
		return nil
	}
	if ttsEngine == nil {
		logger.Warn("speaker alerts enabled but no TTS engine is configured (voice.tts.model_path)")
		return nil
	}
	return &alerts.SpeakerNotifier{TTS: ttsEngine, PlayerPath: cfg.PlayerPath, Language: language}
}

// sendTestAlert delivers one harmless, clearly-marked test notification
// through a single named channel — the settings page's "test" button
// (docs/alerts.md), the only way to find out a bot token is wrong, a
// webhook URL is unreachable, or the speaker channel has no working audio
// device *before* a real NOAA alert or threshold crossing silently fails
// to reach anyone. Passes enabled: true to the notifier constructor
// regardless of that channel's own settings-page toggle — being off is
// exactly the state a human tests from before deciding to flip it on.
func sendTestAlert(ctx context.Context, cfg *config.Config, ttsEngine voice.TTSEngine, language string, logger *slog.Logger, channel string) error {
	var notifier alerts.Notifier
	switch channel {
	case "telegram":
		notifier = telegramNotifier(cfg.Alerts.Channels.Telegram, true, logger)
	case "webhook":
		notifier = webhookNotifier(cfg.Alerts.Channels.Webhook, true)
	case "speaker":
		notifier = speakerNotifier(cfg.Alerts.Channels.Speaker, true, ttsEngine, language, logger)
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
	if notifier == nil {
		return fmt.Errorf("channel %q is not configured", channel)
	}
	return notifier.Notify(ctx, alerts.Alert{
		Source:   "test",
		Severity: alerts.SeverityInfo,
		Title:    "Bosun test alert",
		Body:     "This is a test from the settings page — no actual emergency.",
		At:       time.Now(),
	})
}

func collectNotifiers(candidates ...alerts.Notifier) []alerts.Notifier {
	var notifiers []alerts.Notifier
	for _, n := range candidates {
		if n != nil {
			notifiers = append(notifiers, n)
		}
	}
	return notifiers
}

// noaaAlertNotifiers assembles every channel that's both configured
// (config.yaml/.env) and globally enabled (the settings page's NOAA
// toggles) — NOAA is a single source, so unlike threshold rules there's
// no per-rule channel selection to make, just one on/off per channel.
// Re-read on every check rather than cached once, so flipping a settings
// toggle takes effect on the very next tick, not after a restart.
func noaaAlertNotifiers(cfg *config.Config, settingsStore *settings.Store, ttsEngine voice.TTSEngine, logger *slog.Logger) []alerts.Notifier {
	data := settingsStore.Get()
	return collectNotifiers(
		telegramNotifier(cfg.Alerts.Channels.Telegram, data.AlertsTelegramEnabled, logger),
		webhookNotifier(cfg.Alerts.Channels.Webhook, data.AlertsWebhookEnabled),
		speakerNotifier(cfg.Alerts.Channels.Speaker, data.AlertsSpeakerEnabled, ttsEngine, data.DefaultLanguage, logger),
	)
}

// thresholdRuleNotifiers assembles the channels one specific web-managed
// threshold rule (settings.AlertsThresholdRule) has opted into — a
// channel still only fires if it's also configured in
// config.yaml/.env, same "config decides what exists" rule as
// noaaAlertNotifiers, just keyed off the rule's own checkboxes instead of
// a single global toggle.
func thresholdRuleNotifiers(cfg *config.Config, rule settings.AlertsThresholdRule, ttsEngine voice.TTSEngine, language string, logger *slog.Logger) []alerts.Notifier {
	return collectNotifiers(
		telegramNotifier(cfg.Alerts.Channels.Telegram, rule.Telegram, logger),
		webhookNotifier(cfg.Alerts.Channels.Webhook, rule.Webhook),
		speakerNotifier(cfg.Alerts.Channels.Speaker, rule.Speaker, ttsEngine, language, logger),
	)
}

// currentPosition resolves the point NOAA alerts should watch: a fixed
// config.yaml lat/lon, or — with use_gps — whatever the get_gps tool
// reports right now, the point that actually matters for a vehicle that
// moves rather than a fixed value that's only ever right by luck.
func currentPosition(ctx context.Context, registry *tools.Registry, noaaCfg config.AlertsNOAAConfig) (float64, float64, error) {
	if !noaaCfg.UseGPS {
		return noaaCfg.Latitude, noaaCfg.Longitude, nil
	}
	gpsTool, ok := registry.Get("get_gps")
	if !ok {
		return 0, 0, fmt.Errorf("alerts.noaa.use_gps is set but no get_gps tool is registered")
	}
	result, err := gpsTool.Execute(ctx, map[string]any{})
	if err != nil {
		return 0, 0, fmt.Errorf("read GPS position: %w", err)
	}
	data, ok := result.(map[string]any)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected get_gps result shape: %T", result)
	}
	lat, ok := data["latitude"].(float64)
	if !ok {
		return 0, 0, fmt.Errorf("get_gps result missing numeric latitude")
	}
	lon, ok := data["longitude"].(float64)
	if !ok {
		return 0, 0, fmt.Errorf("get_gps result missing numeric longitude")
	}
	return lat, lon, nil
}

// seedThresholdRules converts config.yaml's alerts.thresholds (if any)
// into the settings-managed rule shape, for settings.Load to seed the
// very first time — a one-time migration path, not something read again
// after that (see runThresholdChecker). Every channel starts off so the
// human opts each rule into Telegram/webhook/speaker from the settings
// page explicitly, same as config.yaml only ever provided metric/
// operator/value/title before this feature existed.
func seedThresholdRules(configured []config.AlertsThresholdConfig) []settings.AlertsThresholdRule {
	rules := make([]settings.AlertsThresholdRule, 0, len(configured))
	for _, t := range configured {
		rules = append(rules, settings.AlertsThresholdRule{
			Metric: t.Metric, Operator: t.Operator, Value: t.Value, Title: t.Title,
			SmoothingSamples: 1,
		})
	}
	return rules
}

// runThresholdChecker watches internal/settings.Data.AlertsThresholds —
// web-managed rules, not config.yaml (which only ever seeds that list
// once, see settings.Load's call site above) — against internal/metrics'
// samples, notifying only on a state transition (see
// alerts.ThresholdChecker.Check). Rules are re-read from settingsStore on
// every tick, so adding, editing, or removing one from the settings page
// takes effect on the very next tick, not after a restart — every metric
// name here is exactly whatever metrics.sources already samples, so a
// future custom sensor (a tank level, battery charge) needs no change in
// this function, only a new metrics.sources entry and a new rule from
// the web UI.
func runThresholdChecker(
	ctx context.Context,
	cfg *config.Config,
	settingsStore *settings.Store,
	metricsStore *metrics.Store,
	ttsEngine voice.TTSEngine,
	dataDir string,
	logger *slog.Logger,
) {
	state, err := alerts.LoadThresholdState(dataDir)
	if err != nil {
		logger.Warn("load threshold alert state; starting fresh", "error", err)
		state = map[string]bool{}
	}

	const checkInterval = 30 * time.Second
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data := settingsStore.Get()
			thresholds := make([]alerts.Threshold, 0, len(data.AlertsThresholds))
			for _, rule := range data.AlertsThresholds {
				thresholds = append(thresholds, alerts.Threshold{
					ID:               rule.ID,
					Metric:           rule.Metric,
					Operator:         rule.Operator,
					Value:            rule.Value,
					Title:            rule.Title,
					SmoothingSamples: rule.SmoothingSamples,
					CustomText:       rule.CustomText,
					PlaySiren:        rule.Siren,
					Notifiers:        thresholdRuleNotifiers(cfg, rule, ttsEngine, data.DefaultLanguage, logger),
				})
			}
			checker := alerts.ThresholdChecker{Store: metricsStore, Thresholds: thresholds}
			next, errs := checker.Check(ctx, state)
			for _, err := range errs {
				logger.Warn("threshold alert check", "error", err)
			}
			state = next
			if err := alerts.SaveThresholdState(dataDir, state); err != nil {
				logger.Warn("save threshold alert state", "error", err)
			}
		}
	}
}

// runNOAAChecker polls weather.gov for active alerts covering the current
// position (see currentPosition) and notifies about every one not already
// seen (alerts.CheckNOAA) — US coverage only; a point outside it just
// means an empty result every tick, not an error.
func runNOAAChecker(
	ctx context.Context,
	cfg *config.Config,
	registry *tools.Registry,
	settingsStore *settings.Store,
	ttsEngine voice.TTSEngine,
	dataDir string,
	logger *slog.Logger,
) {
	seen, err := alerts.LoadNOAASeenIDs(dataDir)
	if err != nil {
		logger.Warn("load NOAA alert state; starting fresh", "error", err)
		seen = map[string]bool{}
	}

	interval, err := time.ParseDuration(cfg.Alerts.NOAA.CheckInterval)
	if err != nil || interval <= 0 {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lat, lon, err := currentPosition(ctx, registry, cfg.Alerts.NOAA)
			if err != nil {
				logger.Warn("resolve position for NOAA alerts", "error", err)
				continue
			}
			notifiers := noaaAlertNotifiers(cfg, settingsStore, ttsEngine, logger)
			next, errs := alerts.CheckNOAA(ctx, lat, lon, seen, notifiers)
			for _, err := range errs {
				logger.Warn("NOAA alert check", "error", err)
			}
			seen = next
			if err := alerts.SaveNOAASeenIDs(dataDir, seen); err != nil {
				logger.Warn("save NOAA alert state", "error", err)
			}
		}
	}
}

// buildRegistry also returns the document store (see internal/documents)
// so serveCmd can wire it into the web UI's upload endpoints — document
// ingestion is a human-only, web-UI-only path, never an LLM tool action,
// to keep the tool contract small for weak local models.
func buildRegistry(cfg *config.Config) (*tools.Registry, *documents.Store) {
	docStore := documents.NewStore(cfg.Documents.Path, embeddings.NewClient(&cfg.LLM.Embeddings))
	memoTool := tools.NewMemoTool(&cfg.Memo, &cfg.LLM.Embeddings)
	memoTool.SetDocumentStore(docStore)

	registry := tools.NewRegistry()
	registry.Register(memoTool)
	registry.Register(tools.NewWebSearchTool(&cfg.Online))
	registry.Register(tools.NewWikipediaTool(&cfg.Online))
	registry.Register(tools.NewDirectionsTool(&cfg.Maps, &cfg.Sensors.GPS))
	registry.Register(tools.NewWeatherTool(&cfg.Sensors.Weather))
	registry.Register(tools.NewFridgeTool(&cfg.Sensors.Fridge))
	registry.Register(tools.NewGPSTool(&cfg.Sensors.GPS))
	registry.Register(tools.NewSystemTool(&cfg.Sensors.System))
	if cfg.Sandbox.Enabled {
		registry.Register(tools.NewCodeExecTool(&cfg.Sandbox))
	}
	return registry, docStore
}
