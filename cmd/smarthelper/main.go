// Command smarthelper is the entry point for ai-local-smarthelper.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/mcp"
	"github.com/roman220/ai-local-smarthelper/internal/tools"
)

const version = "0.1.0"

func main() {
	_ = godotenv.Load() // optional .env; missing file is not an error

	root := &cobra.Command{
		Use:   "smarthelper",
		Short: "Local-first AI assistant with hybrid LLM routing and MCP sensor tools",
	}
	root.AddCommand(versionCmd(), mcpCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("ai-local-smarthelper " + version)
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

			registry := tools.NewRegistry()
			registry.Register(tools.NewWeatherTool(&cfg.Sensors.Weather))
			registry.Register(tools.NewFridgeTool(&cfg.Sensors.Fridge))
			registry.Register(tools.NewGPSTool(&cfg.Sensors.GPS))
			registry.Register(tools.NewSystemTool(&cfg.Sensors.System))

			server := mcp.NewServer(cfg.MCP.ServerName, version, registry, logger)
			logger.Info("starting MCP server", "transport", cfg.MCP.Transport, "tools", registry.List())

			return server.Serve(context.Background(), os.Stdin, os.Stdout)
		},
	}
}
