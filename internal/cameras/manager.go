package cameras

import (
	"context"
	"log/slog"
)

// Config is one camera's own settings — internal/config.CameraConfig
// gets converted into this at wiring time (cmd/smarthelper/main.go), so
// this package doesn't need to import internal/config.
type Config struct {
	Name      string
	LabelRU   string
	LabelEN   string
	StreamURL string
}

// Manager holds one Relay per configured camera, looked up by name for
// both the live-view/archive HTTP handlers (internal/webui/cameras.go)
// and the recorder (cmd/smarthelper/main.go's runCameraRecorder).
type Manager struct {
	cameras []Config
	relays  map[string]*Relay
}

// NewManager builds (but doesn't yet start) a Relay for every configured
// camera.
func NewManager(cameras []Config, logger *slog.Logger) *Manager {
	relays := make(map[string]*Relay, len(cameras))
	for _, c := range cameras {
		relays[c.Name] = NewRelay(c.Name, c.StreamURL, logger)
	}
	return &Manager{cameras: cameras, relays: relays}
}

// Start runs every camera's Relay in its own goroutine until ctx is
// cancelled.
func (m *Manager) Start(ctx context.Context) {
	for _, relay := range m.relays {
		go relay.Run(ctx)
	}
}

// List returns every configured camera's identity (not its stream URL —
// that's an internal detail the web UI never needs), for GET
// /api/cameras/list.
func (m *Manager) List() []Config {
	list := make([]Config, len(m.cameras))
	copy(list, m.cameras)
	return list
}

// Relay returns the named camera's Relay, or false if no camera by that
// name is configured.
func (m *Manager) Relay(name string) (*Relay, bool) {
	relay, ok := m.relays[name]
	return relay, ok
}
