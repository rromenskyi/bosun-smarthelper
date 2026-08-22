// Command smarthelper is the entry point for bosun-smarthelper.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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
	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/embeddings"
	"github.com/roman220/bosun-smarthelper/internal/errlog"
	"github.com/roman220/bosun-smarthelper/internal/llm"
	"github.com/roman220/bosun-smarthelper/internal/mcp"
	"github.com/roman220/bosun-smarthelper/internal/metrics"
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
	root.AddCommand(versionCmd(), mcpCmd(), chatCmd(), serveCmd(), errorsCmd(), documentsCmd(), backupCmd(), restoreCmd())

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
			if cfg.Voice.TTS.ModelPath != "" {
				server.SetTTSEngine(&voice.PiperTTS{
					BinaryPath:     cfg.Voice.TTS.BinaryPath,
					ModelPath:      cfg.Voice.TTS.ModelPath,
					EspeakDataPath: cfg.Voice.TTS.EspeakDataPath,
				})
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
	return registry, docStore
}
