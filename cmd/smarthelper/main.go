// Command smarthelper is the entry point for ai-local-smarthelper.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/roman220/ai-local-smarthelper/internal/agent"
	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/documents"
	"github.com/roman220/ai-local-smarthelper/internal/embeddings"
	"github.com/roman220/ai-local-smarthelper/internal/errlog"
	"github.com/roman220/ai-local-smarthelper/internal/llm"
	"github.com/roman220/ai-local-smarthelper/internal/mcp"
	"github.com/roman220/ai-local-smarthelper/internal/settings"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
	"github.com/roman220/ai-local-smarthelper/internal/voice"
	"github.com/roman220/ai-local-smarthelper/internal/webui"
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
	root.AddCommand(versionCmd(), mcpCmd(), chatCmd(), serveCmd(), errorsCmd())

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

			if memoTool, ok := registry.Get("memo"); ok {
				if mt, ok := memoTool.(*tools.MemoTool); ok {
					interval, err := time.ParseDuration(cfg.Memo.TagNormalizeInterval)
					if err != nil || interval <= 0 {
						interval = 5 * time.Minute
					}
					go runTagNormalizer(cmd.Context(), server, mt, router, settingsStore, interval, logger)
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
