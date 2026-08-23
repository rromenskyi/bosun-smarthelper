package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/backup"
	"github.com/roman220/bosun-smarthelper/internal/llm"
	"github.com/roman220/bosun-smarthelper/internal/settings"
	"github.com/roman220/bosun-smarthelper/internal/tools"
	"github.com/roman220/bosun-smarthelper/internal/webui"
)

// runTagNormalizer periodically maps memos' free-form tags onto
// cfg.Memo.CanonicalTags (see internal/tools/memo_tags.go), but only when
// server.TryIdleAfter reports no chat request is in flight and none has
// finished in the last interval either — a busy assistant never falls
// behind because of this, and a user typing a follow-up right after a
// reply never queues behind background maintenance. Stops when ctx is
// cancelled (process shutdown). Always runs (there's no separate on/off
// switch at startup) but is a no-op each tick when
// settingsStore.Get().CanonicalTags is currently empty — so turning the
// feature on later from the settings page (docs/settings.md) takes
// effect without a restart, at the cost of one cheap check per idle tick
// when it's off.
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
