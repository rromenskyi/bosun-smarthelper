package tools

import (
	"context"
	"runtime"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

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
			result["uptime_seconds"] = info.Uptime
			result["boot_time"] = info.BootTime
		}
	}

	if include["cpu"] {
		percent, _ := cpu.PercentWithContext(ctx, 0, false)
		counts, _ := cpu.CountsWithContext(ctx, true)
		result["cpu_percent"] = percent
		result["cpu_cores"] = counts
	}

	if include["memory"] {
		vm, _ := mem.VirtualMemoryWithContext(ctx)
		result["memory"] = map[string]any{
			"total_bytes":     vm.Total,
			"available_bytes": vm.Available,
			"used_bytes":      vm.Used,
			"used_percent":    vm.UsedPercent,
		}
	}

	if include["disk"] {
		usage, _ := disk.UsageWithContext(ctx, "/")
		result["disk"] = map[string]any{
			"total_bytes":  usage.Total,
			"free_bytes":   usage.Free,
			"used_bytes":   usage.Used,
			"used_percent": usage.UsedPercent,
		}
	}

	// Add Go runtime info
	result["go_version"] = runtime.Version()
	result["goroutines"] = runtime.NumGoroutine()

	return result, nil
}
