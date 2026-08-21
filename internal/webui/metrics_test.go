package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/metrics"
)

func openTestMetricsStore(t *testing.T) *metrics.Store {
	t.Helper()
	store, err := metrics.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("metrics.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestServerMetricsDisabledByDefault(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)

	listResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResp, httptest.NewRequest(http.MethodGet, "/api/metrics/list", nil))
	var list map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if metricsList, ok := list["metrics"].([]any); !ok || len(metricsList) != 0 {
		t.Errorf("metrics list = %v, want an empty list when no store is configured", list["metrics"])
	}

	queryResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(queryResp, httptest.NewRequest(http.MethodGet, "/api/metrics?metric=cpu_temp_c", nil))
	if queryResp.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no store is configured", queryResp.Code)
	}

	statusResp := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusResp, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var status map[string]any
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["metrics_enabled"] != false {
		t.Errorf("status metrics_enabled = %v, want false", status["metrics_enabled"])
	}
}

func TestServerMetricsListReturnsStoredMetricNames(t *testing.T) {
	store := openTestMetricsStore(t)
	if err := store.Insert(t.Context(), time.Now(), "cpu_temp_c", 70); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetMetricsStore(store)
	// Labels come from config.MetricsConfig.Sources via main.go, not from
	// anything hardcoded in this package — set here the way main.go would.
	server.SetMetricsLabels(map[string]MetricLabel{
		"cpu_temp_c": {RU: "Температура CPU", EN: "CPU temperature", Unit: "°C"},
	})

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/metrics/list", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Metrics []struct {
			Name    string `json:"name"`
			LabelRU string `json:"label_ru"`
			Unit    string `json:"unit"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Metrics) != 1 || body.Metrics[0].Name != "cpu_temp_c" {
		t.Fatalf("metrics = %+v, want just cpu_temp_c", body.Metrics)
	}
	if body.Metrics[0].LabelRU != "Температура CPU" || body.Metrics[0].Unit != "°C" {
		t.Errorf("cpu_temp_c label/unit = %+v, want the label from SetMetricsLabels", body.Metrics[0])
	}
}

func TestServerMetricsListFallsBackToMetricNameWithoutLabel(t *testing.T) {
	store := openTestMetricsStore(t)
	if err := store.Insert(t.Context(), time.Now(), "some_new_sensor", 1); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetMetricsStore(store)
	// No SetMetricsLabels call at all — a metric with data but no
	// configured label (e.g. right after adding a new source to
	// config.yaml, before also filling in label_ru/label_en) must still
	// show up, just under its own raw name.

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/metrics/list", nil))
	var body struct {
		Metrics []struct {
			Name    string `json:"name"`
			LabelRU string `json:"label_ru"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Metrics) != 1 || body.Metrics[0].LabelRU != "some_new_sensor" {
		t.Errorf("metrics = %+v, want label_ru to fall back to the raw metric name", body.Metrics)
	}
}

func TestServerMetricsQueryReturnsPoints(t *testing.T) {
	store := openTestMetricsStore(t)
	now := time.Now()
	if err := store.Insert(t.Context(), now.Add(-time.Minute), "cpu_temp_c", 60); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := store.Insert(t.Context(), now, "cpu_temp_c", 70); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetMetricsStore(store)

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/metrics?metric=cpu_temp_c&range=1h", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Metric string `json:"metric"`
		Points []struct {
			T int64   `json:"t"`
			V float64 `json:"v"`
		} `json:"points"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Metric != "cpu_temp_c" {
		t.Errorf("metric = %q, want cpu_temp_c", body.Metric)
	}
	if len(body.Points) != 2 {
		t.Fatalf("len(points) = %d, want 2", len(body.Points))
	}
}

func TestServerMetricsQueryRequiresMetricParam(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetMetricsStore(openTestMetricsStore(t))

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when metric is missing", response.Code)
	}
}
