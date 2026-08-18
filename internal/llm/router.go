package llm

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

// Router manages LLM provider selection based on connectivity
type Router struct {
	localClient  *LocalClient
	remoteClient *RemoteClient
	config       *config.LLMConfig
	lastCheck    time.Time
	isOnline     bool
}

// NewRouter creates a new LLM router
func NewRouter(cfg *config.LLMConfig) (*Router, error) {
	localTimeout, _ := time.ParseDuration(cfg.Local.Timeout)
	if localTimeout == 0 {
		localTimeout = 60 * time.Second
	}

	remoteTimeout, _ := time.ParseDuration(cfg.Remote.Timeout)
	if remoteTimeout == 0 {
		remoteTimeout = 30 * time.Second
	}

	localClient := NewLocalClient(cfg.Local.BaseURL, cfg.Local.Model, localTimeout)

	remoteClient, err := NewRemoteClient(
		cfg.Remote.BaseURL,
		cfg.Remote.Model,
		cfg.Remote.APIKeyEnv,
		cfg.Remote.Organization,
		remoteTimeout,
	)
	if err != nil {
		// Remote client creation failed (e.g., no API key) - continue with local only
		remoteClient = nil
	}

	return &Router{
		localClient:  localClient,
		remoteClient: remoteClient,
		config:       cfg,
		isOnline:     false,
	}, nil
}

// CheckConnectivity performs a connectivity check
func (r *Router) CheckConnectivity(ctx context.Context) bool {
	checkTimeout, _ := time.ParseDuration(r.config.Router.CheckTimeout)
	if checkTimeout == 0 {
		checkTimeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "HEAD", r.config.Router.CheckTarget, nil)
	if err != nil {
		r.isOnline = false
		return false
	}

	client := &http.Client{Timeout: checkTimeout}
	resp, err := client.Do(req)
	if err != nil {
		r.isOnline = false
		return false
	}
	defer resp.Body.Close()

	r.isOnline = resp.StatusCode < 500
	r.lastCheck = time.Now()
	return r.isOnline
}

// IsOnline returns the last known connectivity status
func (r *Router) IsOnline() bool {
	return r.isOnline
}

// GetClient returns the appropriate client based on connectivity and config
func (r *Router) GetClient(ctx context.Context) (Client, error) {
	// Check if we need to re-check connectivity
	checkInterval, _ := time.ParseDuration(r.config.Router.CheckInterval)
	if checkInterval == 0 {
		checkInterval = 30 * time.Second
	}

	if time.Since(r.lastCheck) > checkInterval {
		go r.CheckConnectivity(context.Background()) // Async update
	}

	preferRemote := r.config.Router.PreferRemote

	if preferRemote && r.isOnline && r.remoteClient != nil {
		return r.remoteClient, nil
	}

	// Fallback to local
	return r.localClient, nil
}

// Chat routes the request to the appropriate provider
func (r *Router) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	client, err := r.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}

	resp, err := client.Chat(ctx, messages, tools)
	if err != nil {
		// If remote fails and we haven't tried local, try local
		if client.Provider() == "remote" && r.localClient != nil {
			return r.localClient.Chat(ctx, messages, tools)
		}
		return nil, err
	}

	return resp, nil
}

// LocalClient returns the local client for direct access
func (r *Router) LocalClient() *LocalClient {
	return r.localClient
}

// RemoteClient returns the remote client for direct access
func (r *Router) RemoteClient() *RemoteClient {
	return r.remoteClient
}
