package tools

import (
	"reflect"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

func TestRegistryListIsStable(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewWeatherTool(&config.WeatherConfig{}))
	registry.Register(NewGPSTool(&config.GPSConfig{}))
	registry.Register(NewFridgeTool(&config.FridgeConfig{}))

	want := []string{"get_fridge_temp", "get_gps", "get_weather"}
	if got := registry.List(); !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
}

func TestRegistryAvailableListHidesNetworkToolsOffline(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewWeatherTool(&config.WeatherConfig{Type: "open_meteo"}))
	registry.Register(NewGPSTool(&config.GPSConfig{Type: "mock"}))

	if got, want := registry.AvailableList(false), []string{"get_gps"}; !reflect.DeepEqual(got, want) {
		t.Errorf("offline AvailableList() = %v, want %v", got, want)
	}
	if got, want := registry.AvailableList(true), []string{"get_gps", "get_weather"}; !reflect.DeepEqual(got, want) {
		t.Errorf("online AvailableList() = %v, want %v", got, want)
	}
}
