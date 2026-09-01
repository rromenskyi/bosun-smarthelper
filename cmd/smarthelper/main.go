// Command smarthelper is the entry point for bosun-smarthelper.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/roman220/bosun-smarthelper/internal/agent"
	"github.com/roman220/bosun-smarthelper/internal/backup"
	"github.com/roman220/bosun-smarthelper/internal/cameras"
	"github.com/roman220/bosun-smarthelper/internal/chatfiles"
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
			registry, _, _, _, _ := buildRegistry(cfg, logger)

			server := mcp.NewServer(cfg.MCP.ServerName, version, registry, logger)
			server.SetErrorLog(openErrorLog(cfg, logger))
			logger.Info("starting MCP server", "transport", cfg.MCP.Transport, "tools", registry.List())

			return server.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
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

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			router, err := llm.NewRouter(&cfg.LLM)
			if err != nil {
				return fmt.Errorf("create LLM router: %w", err)
			}
			router.SetLogger(logger)
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
				NameRU:               cfg.Assistant.NameRU,
				NameEN:               cfg.Assistant.NameEN,
				StylePrompt:          cfg.Assistant.StylePrompt,
				DefaultLanguage:      cfg.Web.DefaultLanguage,
				RemoteTemperature:    cfg.LLM.Remote.Temperature,
				LocalTemperature:     cfg.LLM.Local.Temperature,
				CanonicalTags:        cfg.Memo.CanonicalTags,
				AlertsThresholds:     seedThresholdRules(cfg.Alerts.Thresholds),
				DynamicTopicsEnabled: true,
			})
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}
			live := settingsStore.Get()
			router.SetTemperatures(live.RemoteTemperature, live.LocalTemperature)

			registry, docStore, adventureStore, fileDumpStore, chatFilesStore := buildRegistry(cfg, logger)
			ag := agent.New(router, registry, router.NetworkAvailable)
			ag.SetPersona(live.NameRU, live.NameEN, live.StylePrompt)
			// Shared with every background scheduler started below
			// (runTagNormalizer, runBackupScheduler, etc.) — one process,
			// one error log, so `smarthelper errors` shows a background
			// job's failure the same way it already shows a tool/LLM one,
			// not just what happens inside a chat request.
			errLog := openErrorLog(cfg, logger)
			ag.SetErrorLog(errLog)
			if docStore != nil {
				ag.SetTopicsProvider(docStore)
			}
			ag.SetDynamicTopicsEnabled(live.DynamicTopicsEnabled)

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
			if adventureStore != nil {
				server.SetAdventureStore(adventureStore)
				server.SetAdventureNarrator(router, cfg.Adventure.NarrateLocal, cfg.Adventure.NarrateRemote)
			}
			if cfg.Adventure.MediaDir != "" {
				server.SetAdventureMediaDir(cfg.Adventure.MediaDir)
			}
			if fileDumpStore != nil {
				server.SetFileDumpStore(fileDumpStore)
			}
			if chatFilesStore != nil {
				server.SetChatFilesStore(chatFilesStore)
				const chatFilesTTL = 1 * time.Hour
				go chatfiles.Run(cmd.Context(), chatFilesStore, 10*time.Minute, chatFilesTTL, logger, errLog)
			}
			var ttsEngine voice.TTSEngine
			if cfg.Voice.TTS.ModelPath != "" {
				primary := &voice.PiperTTS{
					BinaryPath:     cfg.Voice.TTS.BinaryPath,
					ModelPath:      cfg.Voice.TTS.ModelPath,
					EspeakDataPath: cfg.Voice.TTS.EspeakDataPath,
				}
				ttsEngine = primary
				if cfg.Voice.TTS.EnglishModelPath != "" {
					ttsEngine = &voice.LanguageAwareTTS{
						Russian: primary,
						English: &voice.PiperTTS{
							BinaryPath:     cfg.Voice.TTS.BinaryPath,
							ModelPath:      cfg.Voice.TTS.EnglishModelPath,
							EspeakDataPath: cfg.Voice.TTS.EspeakDataPath,
						},
					}
				}
				server.SetTTSEngine(ttsEngine)
			}
			// Remote is preferred while online (RoutedSTT), falling back to
			// local on any failure — same shape as the chat router just
			// above, driven by the same connectivity check, not a separate
			// manual setting. See config.STTConfig's doc comment for why.
			var localSTT, remoteSTT voice.STTEngine
			if cfg.Voice.STT.BaseURL != "" {
				localSTT = &voice.WhisperCppSTT{
					BaseURL:  cfg.Voice.STT.BaseURL,
					Language: cfg.Voice.STT.Language,
				}
			}
			if cfg.Voice.STT.Remote.BaseURL != "" {
				if cfg.Voice.STT.Remote.APIKeyEnv == "" {
					logger.Warn("voice.stt.remote.base_url is set but api_key_env is empty; remote STT disabled")
				} else if apiKey := os.Getenv(cfg.Voice.STT.Remote.APIKeyEnv); apiKey == "" {
					logger.Warn("voice.stt.remote.api_key_env is set but the env var is empty; remote STT disabled", "env", cfg.Voice.STT.Remote.APIKeyEnv)
				} else {
					remoteSTT = &voice.RemoteSTT{
						BaseURL:  cfg.Voice.STT.Remote.BaseURL,
						Model:    cfg.Voice.STT.Remote.Model,
						APIKey:   apiKey,
						Language: cfg.Voice.STT.Language,
					}
				}
			}
			switch {
			case remoteSTT != nil && localSTT != nil:
				server.SetSTTEngine(&voice.RoutedSTT{
					Remote:           remoteSTT,
					Local:            localSTT,
					NetworkAvailable: router.NetworkAvailable,
					Logger:           logger,
				})
			case remoteSTT != nil:
				server.SetSTTEngine(remoteSTT)
			case localSTT != nil:
				server.SetSTTEngine(localSTT)
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
						go runThresholdChecker(cmd.Context(), cfg, settingsStore, metricsStore, ttsEngine, alertsDataDir, logger, errLog)
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
					go runTagNormalizer(cmd.Context(), server, mt, router, settingsStore, interval, logger, errLog)

					mergeInterval, err := time.ParseDuration(cfg.Memo.MetricMergeCheckInterval)
					if err != nil || mergeInterval <= 0 {
						mergeInterval = 24 * time.Hour
					}
					go runMetricMergeChecker(cmd.Context(), server, mt, router, mergeInterval, logger, errLog)
				}
			}

			if s3cfg, err := resolveBackupS3(cfg); err != nil {
				logger.Info("backup not configured", "reason", err)
			} else if dataDir, err := resolveDataDir(cfg.Backup.DataDir); err != nil {
				logger.Warn("could not resolve backup data directory; backup disabled", "error", err)
			} else {
				server.SetBackupConfig(&s3cfg, dataDir)
				go runBackupScheduler(cmd.Context(), server, settingsStore, s3cfg, dataDir, logger, errLog)
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
					go runNOAAChecker(cmd.Context(), cfg, registry, settingsStore, ttsEngine, alertsDataDir, logger, errLog)
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

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			router, err := llm.NewRouter(&cfg.LLM)
			if err != nil {
				return fmt.Errorf("create LLM router: %w", err)
			}
			router.SetLogger(logger)
			// Refresh connectivity before routing: configurable remote endpoint
			// when online, falling back to the local model when offline.
			router.CheckConnectivity(cmd.Context())

			registry, _, _, _, _ := buildRegistry(cfg, logger)
			ag := agent.New(router, registry, router.NetworkAvailable)
			ag.SetPersona(cfg.Assistant.NameRU, cfg.Assistant.NameEN, cfg.Assistant.StylePrompt)
			ag.SetErrorLog(openErrorLog(cfg, logger))

			answer, _, err := ag.Ask(cmd.Context(), strings.Join(args, " "))
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
		Short: "Show recent tool/LLM/background-job failures recorded for review (sandboxd's own log is separate — see docs/sandbox.md)",
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

			// sandboxd is a separate process/container from `serve` (see
			// docs/sandbox.md), so it gets its own error log rather than
			// sharing the main one — backed by StateDir, the one path
			// sandboxd has that's actually bind-mounted/persisted (see
			// docker-compose.yml's ./data/sandbox mount); its default
			// per-user home directory is not.
			errLog, err := errlog.Open(filepath.Join(cfg.Sandbox.StateDir, "errors.jsonl"))
			if err != nil {
				logger.Warn("could not open sandboxd error log; failures will not be recorded", "error", err)
				errLog = nil
			}

			if err := sandbox.Reconcile(cmd.Context(), server.Runner, tracker); err != nil {
				logger.Warn("reconcile sandbox session state against running containers", "error", err)
				errLog.Record("sandbox_reaper", "reconcile", err)
			}
			go sandbox.Run(cmd.Context(), server, 2*time.Minute, sessionTTL, logger, errLog)

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
