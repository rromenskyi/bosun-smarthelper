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
	if cfg.Web.Bind != "127.0.0.1:8080" {
		t.Errorf("web bind = %q, want loopback default", cfg.Web.Bind)
	}
	if cfg.Web.DefaultLanguage != "ru" {
		t.Errorf("web default_language = %q, want ru", cfg.Web.DefaultLanguage)
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
}
