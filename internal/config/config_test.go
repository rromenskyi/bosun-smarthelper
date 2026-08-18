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
	if cfg.Sensors.Weather.Type != "mock" {
		t.Errorf("weather.type = %q, want mock", cfg.Sensors.Weather.Type)
	}
}
