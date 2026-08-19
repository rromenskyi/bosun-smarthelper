package tools

import (
	"context"
	"math"
	"runtime"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

// roundPercent rounds a raw percentage to a whole number — a captain's log
// doesn't need "5.21%", and the LLM was otherwise inventing its own
// (inconsistent) rounding from full-precision floats.
func roundPercent(v float64) float64 {
	return math.Round(v)
}

// bytesToGB rounds a raw byte count to one decimal place of GB, so the LLM
// reports "7.7 GB" consistently instead of computing (and varying) its own
// "7.69 GB"-style conversion from a raw byte count every time.
func bytesToGB(v uint64) float64 {
	return math.Round(float64(v)/1e9*10) / 10
}

// SystemTool provides system metrics
type SystemTool struct {
	config *config.SystemConfig
}

// NewSystemTool creates a new system tool
func NewSystemTool(cfg *config.SystemConfig) *SystemTool {
	return &SystemTool{config: cfg}
}

func (t *SystemTool) Name() string {
	return "get_system_info"
}

func (t *SystemTool) Description() string {
	return "Get system metrics: CPU, memory, disk, uptime"
}

func (t *SystemTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"include": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Metrics to include: cpu, memory, disk, host (default: all)",
			},
		},
		"additionalProperties": false,
	}
}

func (t *SystemTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	include := map[string]bool{
		"cpu":    true,
		"memory": true,
		"disk":   true,
		"host":   true,
	}

	if inc, ok := args["include"].([]any); ok {
		for k := range include {
			include[k] = false
		}
		for _, v := range inc {
			if s, ok := v.(string); ok {
				include[s] = true
			}
		}
	}

	result := map[string]any{"source": "native"}

	if include["host"] {
		info, err := host.InfoWithContext(ctx)
		if err == nil {
			result["hostname"] = info.Hostname
			result["os"] = info.OS
			result["platform"] = info.Platform
			result["platform_version"] = info.PlatformVersion
			// A human-readable duration, not raw seconds — the LLM was
			// otherwise doing its own (inconsistent) "96512s ~ a day" math.
			result["uptime"] = (time.Duration(info.Uptime) * time.Second).Round(time.Minute).String()
			result["boot_time"] = info.BootTime
		}
	}

	if include["cpu"] {
		percent, _ := cpu.PercentWithContext(ctx, 0, false)
		counts, _ := cpu.CountsWithContext(ctx, true)
		rounded := make([]float64, len(percent))
		for i, p := range percent {
			rounded[i] = roundPercent(p)
		}
		result["cpu_percent"] = rounded
		result["cpu_cores"] = counts
	}

	if include["memory"] {
		vm, _ := mem.VirtualMemoryWithContext(ctx)
		result["memory"] = map[string]any{
			"total_gb":     bytesToGB(vm.Total),
			"available_gb": bytesToGB(vm.Available),
			"used_gb":      bytesToGB(vm.Used),
			"used_percent": roundPercent(vm.UsedPercent),
		}
	}

	if include["disk"] {
		usage, _ := disk.UsageWithContext(ctx, "/")
		result["disk"] = map[string]any{
			"total_gb":     bytesToGB(usage.Total),
			"free_gb":      bytesToGB(usage.Free),
			"used_gb":      bytesToGB(usage.Used),
			"used_percent": roundPercent(usage.UsedPercent),
		}
	}

	// Add Go runtime info
	result["go_version"] = runtime.Version()
	result["goroutines"] = runtime.NumGoroutine()

	return result, nil
}
