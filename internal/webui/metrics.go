package webui

import (
	"net/http"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/metrics"
)

// MetricLabel is a metric's display info for the dashboard's metric picker
// and chart headers.
type MetricLabel struct {
	RU, EN, Unit string
}

// SetMetricsStore wires in the local monitoring dashboard (docs/monitoring.md).
// Optional: nil (the default) means GET /api/metrics/list always returns an
// empty list and GET /api/metrics 404s, so the dashboard button can just
// hide itself.
func (s *Server) SetMetricsStore(store *metrics.Store) {
	s.metricsStore = store
}

// SetMetricsLabels wires in each metric's display label/unit, built from
// config.MetricsConfig.Sources (not hardcoded here) so a newly configured
// sensor gets a proper label without a code change — see docs/monitoring.md.
func (s *Server) SetMetricsLabels(labels map[string]MetricLabel) {
	s.metricsLabels = labels
}

func (s *Server) handleMetricsList(w http.ResponseWriter, r *http.Request) {
	if s.metricsStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"metrics": []string{}})
		return
	}
	names, err := s.metricsStore.Metrics(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list metrics"})
		return
	}
	type metricInfo struct {
		Name string `json:"name"`
		RU   string `json:"label_ru"`
		EN   string `json:"label_en"`
		Unit string `json:"unit"`
	}
	infos := make([]metricInfo, 0, len(names))
	for _, name := range names {
		label := s.metricsLabels[name]
		if label.RU == "" {
			label.RU, label.EN = name, name
		}
		infos = append(infos, metricInfo{Name: name, RU: label.RU, EN: label.EN, Unit: label.Unit})
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": infos})
}

// rangeDurations maps the dashboard's range picker values to a lookback
// window and a point budget — a 30-day chart doesn't need per-second
// resolution, so wider ranges get a smaller point cap (metrics.Store
// buckets/averages down to it server-side).
var rangeDurations = map[string]struct {
	Since     time.Duration
	MaxPoints int
}{
	"1h":  {time.Hour, 720},
	"24h": {24 * time.Hour, 1440},
	"7d":  {7 * 24 * time.Hour, 2000},
	"30d": {30 * 24 * time.Hour, 2000},
}

func (s *Server) handleMetricsQuery(w http.ResponseWriter, r *http.Request) {
	if s.metricsStore == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "metrics are not available"})
		return
	}
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "metric is required"})
		return
	}
	window, ok := rangeDurations[r.URL.Query().Get("range")]
	if !ok {
		window = rangeDurations["24h"]
	}

	points, err := s.metricsStore.Query(r.Context(), metric, time.Now().Add(-window.Since), window.MaxPoints)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not query metric"})
		return
	}
	type point struct {
		T int64   `json:"t"`
		V float64 `json:"v"`
	}
	out := make([]point, len(points))
	for i, p := range points {
		out[i] = point{T: p.Time.Unix(), V: p.Value}
	}
	writeJSON(w, http.StatusOK, map[string]any{"metric": metric, "points": out})
}
