package webui

import (
	"fmt"
	"net/http"

	"github.com/roman220/bosun-smarthelper/internal/tools"
)

// SetToolRegistry wires in direct, LLM-free access to a small allowlisted
// set of read-only sensor tools (see quickTools/handleQuickTool) for the
// web UI's quick-access chips. Bypassing the LLM entirely for these means
// there's no conversation turn for a weak local model to mis-paraphrase,
// or to skip calling the tool at all and instead copy a stale reading
// still sitting in session history — confirmed live, repeatedly, for the
// system-status chip. The reading is rendered by a fixed Go template
// instead of an LLM turn, and this endpoint never writes to session
// history, so it can't contaminate a later answer either.
func (s *Server) SetToolRegistry(registry *tools.Registry) {
	s.toolRegistry = registry
}

// quickTools is the allowlist for handleQuickTool — deliberately small and
// explicit, not "every registered tool": this endpoint bypasses the LLM's
// own tool_choice/availability gating entirely, so it's reserved for
// simple, narration-free numeric readouts where an LLM's summarizing
// isn't adding anything (unlike weather's multi-day narrative, or memo
// search's judgment calls).
var quickTools = map[string]bool{
	"get_system_info": true,
	"get_gps":         true,
}

func (s *Server) handleQuickTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("tool")
	if !quickTools[name] {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown quick tool"})
		return
	}
	if s.toolRegistry == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "tools are not available"})
		return
	}
	tool, ok := s.toolRegistry.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tool not registered"})
		return
	}
	lang := "ru"
	if r.URL.Query().Get("lang") == "en" {
		lang = "en"
	}

	result, err := tool.Execute(r.Context(), map[string]any{})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"answer": quickToolError(lang, err)})
		return
	}
	data, _ := result.(map[string]any)

	var answer string
	switch name {
	case "get_system_info":
		answer = formatSystemInfo(lang, data)
	case "get_gps":
		answer = formatGPS(lang, data)
	}
	writeJSON(w, http.StatusOK, map[string]string{"answer": answer})
}

func quickToolError(lang string, err error) string {
	if lang == "en" {
		return fmt.Sprintf("Could not read this sensor: %s", err)
	}
	return fmt.Sprintf("Не удалось получить данные с датчика: %s", err)
}

func formatSystemInfo(lang string, data map[string]any) string {
	uptime, _ := data["uptime"].(string)
	cpu, _ := data["cpu"].(map[string]any)
	memory, _ := data["memory"].(map[string]any)
	disk, _ := data["disk"].(map[string]any)

	cpuPercent, _ := cpu["used_percent"].(float64)
	cpuTemp, hasCPUTemp := cpu["temp_c"].(float64)
	memUsed, _ := memory["used_gb"].(float64)
	memTotal, _ := memory["total_gb"].(float64)
	diskFree, _ := disk["free_gb"].(float64)
	diskTotal, _ := disk["total_gb"].(float64)
	diskUsedPercent, _ := disk["used_percent"].(float64)

	temp := ""
	if hasCPUTemp {
		temp = fmt.Sprintf(" (%.0f°C)", cpuTemp)
	}
	if lang == "en" {
		return fmt.Sprintf(
			"Uptime: %s. CPU: %.0f%%%s. Memory: %.0f of %.0f GB used. Disk: %.0f GB free of %.0f GB (%.0f%% used).",
			uptime, cpuPercent, temp, memUsed, memTotal, diskFree, diskTotal, diskUsedPercent,
		)
	}
	return fmt.Sprintf(
		"Аптайм: %s. CPU: %.0f%%%s. Память: занято %.0f из %.0f ГБ. Диск: свободно %.0f из %.0f ГБ (занято %.0f%%).",
		uptime, cpuPercent, temp, memUsed, memTotal, diskFree, diskTotal, diskUsedPercent,
	)
}

func formatGPS(lang string, data map[string]any) string {
	lat, _ := data["latitude"].(float64)
	lon, _ := data["longitude"].(float64)
	speed, _ := data["speed_kmh"].(float64)
	altitude, hasAltitude := data["altitude_m"].(float64)

	altitudePart := ""
	if hasAltitude {
		if lang == "en" {
			altitudePart = fmt.Sprintf(" Altitude: %.0f m.", altitude)
		} else {
			altitudePart = fmt.Sprintf(" Высота: %.0f м.", altitude)
		}
	}
	if lang == "en" {
		return fmt.Sprintf("Position: %.5f, %.5f. Speed: %.0f km/h.%s", lat, lon, speed, altitudePart)
	}
	return fmt.Sprintf("Координаты: %.5f, %.5f. Скорость: %.0f км/ч.%s", lat, lon, speed, altitudePart)
}
