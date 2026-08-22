package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/tools"
)

// sample is one metric reading taken at collection time.
type sample struct {
	Metric string
	Value  float64
}

// Collector periodically samples a config-defined set of sensors — reusing
// the exact same tool implementations the chat agent calls, not a separate
// reading path — and writes them to a Store. What to sample is data
// (config.MetricSource), not code: a new sensor is a config.yaml addition,
// not a Go change. See docs/monitoring.md.
type Collector struct {
	store    *Store
	registry *tools.Registry
	sources  []config.MetricSource
	logger   *slog.Logger
}

func NewCollector(store *Store, registry *tools.Registry, sources []config.MetricSource, logger *slog.Logger) *Collector {
	return &Collector{store: store, registry: registry, sources: sources, logger: logger}
}

// Run samples every interval and prunes samples older than retention once
// an hour, until ctx is cancelled. Meant to run in its own goroutine.
func (c *Collector) Run(ctx context.Context, interval, retention time.Duration) {
	sampleTicker := time.NewTicker(interval)
	defer sampleTicker.Stop()
	pruneTicker := time.NewTicker(time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sampleTicker.C:
			c.collectOnce(ctx)
		case <-pruneTicker.C:
			if err := c.store.Prune(ctx, time.Now().Add(-retention)); err != nil {
				c.logger.Warn("metrics prune failed", "error", err)
			}
		}
	}
}

func (c *Collector) collectOnce(ctx context.Context) {
	now := time.Now()
	for _, s := range sampleAll(ctx, c.registry, c.sources) {
		if err := c.store.Insert(ctx, now, s.Metric, s.Value); err != nil {
			c.logger.Warn("metrics insert failed", "metric", s.Metric, "error", err)
		}
	}
}

// sampleAll runs each configured source's tool call (once per distinct
// tool+args pair, even if several sources read from the same call — e.g.
// cpu_temp_c and cpu_percent both come from one get_system_info call) and
// extracts each source's field. A tool erroring (GPS with no fix yet,
// fridge sensor unreachable) just means every metric reading from that
// call is skipped for this tick, not a collection failure — no fix right
// now doesn't mean no fix ever, and the next tick tries again.
func sampleAll(ctx context.Context, registry *tools.Registry, sources []config.MetricSource) []sample {
	type cacheKey struct{ tool, args string }
	cache := make(map[cacheKey]any)

	var out []sample
	for _, src := range sources {
		tool, ok := registry.Get(src.Tool)
		if !ok {
			continue
		}
		key := cacheKey{src.Tool, fmt.Sprintf("%v", src.Args)}
		result, cached := cache[key]
		if !cached {
			if r, err := tool.Execute(ctx, src.Args); err == nil {
				result = r
			}
			cache[key] = result
		}
		if result == nil {
			continue
		}
		m, ok := result.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := lookupField(m, src.Field)
		if !ok {
			continue
		}
		value, ok := numericValue(raw, src.Aggregate)
		if !ok {
			continue
		}
		out = append(out, sample{src.Metric, value})
	}
	return out
}

// lookupField resolves a dot-separated path (e.g. "memory.used_percent")
// into a tool's map[string]any result.
func lookupField(result map[string]any, path string) (any, bool) {
	var cur any = result
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// numericValue converts a field's raw value into the single float64 to
// store, applying aggregate ("" for a plain number, "avg" to average a
// []float64 — e.g. per-core cpu_percent — into one reading).
func numericValue(raw any, aggregate string) (float64, bool) {
	if aggregate == "avg" {
		values, ok := raw.([]float64)
		if !ok || len(values) == 0 {
			return 0, false
		}
		var sum float64
		for _, v := range values {
			sum += v
		}
		return sum / float64(len(values)), true
	}
	value, ok := raw.(float64)
	return value, ok
}
