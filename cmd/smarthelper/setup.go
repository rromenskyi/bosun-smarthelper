package main

import (
	"log/slog"

	"github.com/roman220/bosun-smarthelper/internal/adventure"
	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/embeddings"
	"github.com/roman220/bosun-smarthelper/internal/errlog"
	"github.com/roman220/bosun-smarthelper/internal/filedump"
	"github.com/roman220/bosun-smarthelper/internal/tools"
)

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

// buildRegistry also returns the document store (see internal/documents)
// so serveCmd can wire it into the web UI's upload endpoints — document
// ingestion is a human-only, web-UI-only path, never an LLM tool action,
// to keep the tool contract small for weak local models. It also
// returns the adventure store (nil unless cfg.Adventure.Enabled) so
// serveCmd can wire it into the web UI's session-management endpoints
// and game-mode chat branch (see docs/adventure.md) — the adventure
// tool registered here is the opportunistic, LLM-decides path; game
// mode's own direct-to-store path (bypassing the tool loop) is where
// cfg.Adventure.NarrateLocal/NarrateRemote actually applies. It also
// returns the file dump store (nil unless cfg.FileDump.Path is set —
// see docs/filedump.md), a human-only, web-UI-only feature like
// documents, never exposed as an LLM tool.
func buildRegistry(cfg *config.Config, logger *slog.Logger) (*tools.Registry, *documents.Store, *adventure.Store, *filedump.Store) {
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

	var adventureStore *adventure.Store
	if cfg.Adventure.Enabled {
		store, err := adventure.Open("")
		if err != nil {
			logger.Warn("could not open adventure store; adventure_game tool disabled", "error", err)
		} else {
			adventureStore = store
			registry.Register(adventure.NewTool(store))
		}
	}

	var fileDumpStore *filedump.Store
	if cfg.FileDump.Path != "" {
		store, err := filedump.NewStore(cfg.FileDump.Path)
		if err != nil {
			logger.Warn("could not open file dump store; file dump disabled", "error", err)
		} else {
			fileDumpStore = store
		}
	}

	return registry, docStore, adventureStore, fileDumpStore
}
