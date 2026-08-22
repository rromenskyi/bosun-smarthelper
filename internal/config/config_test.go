package config

import "testing"

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.LLM.Remote.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("remote base_url = %q, want default", cfg.LLM.Remote.BaseURL)
	}
	if cfg.LLM.Local.Model == "" {
		t.Error("expected a default local model")
	}
	if cfg.LLM.Local.APIFormat != "ollama" {
		t.Errorf("local api_format = %q, want ollama", cfg.LLM.Local.APIFormat)
	}
	if !cfg.LLM.Local.SupportsTools {
		t.Error("local supports_tools = false, want true")
	}
	if !cfg.LLM.Local.Stream {
		t.Error("local stream = false, want true")
	}
	if cfg.Web.Bind != "127.0.0.1:8080" {
		t.Errorf("web bind = %q, want loopback default", cfg.Web.Bind)
	}
	if cfg.Web.DefaultLanguage != "ru" {
		t.Errorf("web default_language = %q, want ru", cfg.Web.DefaultLanguage)
	}
	if cfg.Web.History.Local.Turns != 4 || cfg.Web.History.Local.MaxChars != 4000 {
		t.Errorf("web.history.local = %+v, want small budget for the weak local model", cfg.Web.History.Local)
	}
	if cfg.Web.History.Remote.Turns != 40 || cfg.Web.History.Remote.MaxChars != 60000 {
		t.Errorf("web.history.remote = %+v, want generous budget for the remote model", cfg.Web.History.Remote)
	}
	if cfg.LLM.Remote.MaxRetries != 5 {
		t.Errorf("remote max_retries = %d, want 5", cfg.LLM.Remote.MaxRetries)
	}
	if cfg.Sensors.Weather.Type != "mock" {
		t.Errorf("weather.type = %q, want mock", cfg.Sensors.Weather.Type)
	}
	if cfg.Sensors.GPS.MockLatitude != 40.7608 || cfg.Sensors.GPS.MockLongitude != -111.8910 {
		t.Errorf("mock GPS = %v,%v, want Salt Lake City", cfg.Sensors.GPS.MockLatitude, cfg.Sensors.GPS.MockLongitude)
	}
	if !cfg.Metrics.Enabled {
		t.Error("metrics.enabled = false, want true")
	}
	if len(cfg.Metrics.Sources) == 0 {
		t.Fatal("metrics.sources decoded empty — the default source list didn't survive Viper/mapstructure decoding")
	}
	var cpuTemp *MetricSource
	for i, src := range cfg.Metrics.Sources {
		if src.Metric == "cpu_temp_c" {
			cpuTemp = &cfg.Metrics.Sources[i]
		}
	}
	if cpuTemp == nil {
		t.Fatal("metrics.sources has no cpu_temp_c entry")
	}
	if cpuTemp.Tool != "get_system_info" || cpuTemp.Field != "cpu.temp_c" || cpuTemp.LabelRU != "Температура CPU" {
		t.Errorf("cpu_temp_c source = %+v, want tool=get_system_info field=cpu.temp_c label_ru=Температура CPU", cpuTemp)
	}
	include, ok := cpuTemp.Args["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "cpu" {
		t.Errorf("cpu_temp_c source Args[\"include\"] = %v, want [\"cpu\"]", cpuTemp.Args["include"])
	}
}
