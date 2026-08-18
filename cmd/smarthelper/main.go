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
	"github.com/roman220/ai-local-smarthelper/internal/llm"
	"github.com/roman220/ai-local-smarthelper/internal/mcp"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
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
	root.AddCommand(versionCmd(), mcpCmd(), chatCmd(), serveCmd())

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
			registry := buildRegistry(cfg)

			server := mcp.NewServer(cfg.MCP.ServerName, version, registry, logger)
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

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			registry := buildRegistry(cfg)
			ag := agent.New(router, registry, router.NetworkAvailable)
			ag.SetPersona(cfg.Assistant.NameRU, cfg.Assistant.NameEN, cfg.Assistant.StylePrompt)
			server := webui.NewServer(ag, func() webui.Status {
				online := router.NetworkAvailable(context.Background())
				return webui.Status{
					Online:         online,
					Provider:       router.ActiveProvider(),
					AvailableTools: registry.AvailableList(online),
				}
			}, requestTimeout, cfg.Web.DefaultLanguage, logger, webui.SessionOptions{
				Local:       webui.HistoryBudget{Turns: cfg.Web.History.Local.Turns, MaxChars: cfg.Web.History.Local.MaxChars},
				Remote:      webui.HistoryBudget{Turns: cfg.Web.History.Remote.Turns, MaxChars: cfg.Web.History.Remote.MaxChars},
				TTL:         sessionTTL,
				MaxSessions: cfg.Web.MaxSessions,
			})

			logger.Info("starting web interface", "address", cfg.Web.Bind)
			return server.Serve(cmd.Context(), cfg.Web.Bind)
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

			ag := agent.New(router, buildRegistry(cfg), router.NetworkAvailable)
			ag.SetPersona(cfg.Assistant.NameRU, cfg.Assistant.NameEN, cfg.Assistant.StylePrompt)

			answer, err := ag.Ask(cmd.Context(), strings.Join(args, " "))
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			fmt.Println(answer)
			return nil
		},
	}
}

func buildRegistry(cfg *config.Config) *tools.Registry {
	registry := tools.NewRegistry()
	registry.Register(tools.NewMemoTool(&cfg.Memo))
	registry.Register(tools.NewWebSearchTool(&cfg.Online))
	registry.Register(tools.NewWikipediaTool(&cfg.Online))
	registry.Register(tools.NewWeatherTool(&cfg.Sensors.Weather))
	registry.Register(tools.NewFridgeTool(&cfg.Sensors.Fridge))
	registry.Register(tools.NewGPSTool(&cfg.Sensors.GPS))
	registry.Register(tools.NewSystemTool(&cfg.Sensors.System))
	return registry
}
