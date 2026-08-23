package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

// Router manages LLM provider selection based on connectivity
type Router struct {
	localClient        *LocalClient
	remoteClient       *RemoteClient
	config             *config.LLMConfig
	lastCheck          time.Time
	isOnline           bool
	lastProvider       string
	providerActive     bool
	stateMu            sync.RWMutex
	checkMu            sync.Mutex
	remoteMaxRetries   int
	remoteRetryBackoff time.Duration
	// providerOverride forces a specific provider regardless of
	// connectivity/prefer_remote — "" means automatic (the default).
	// Deliberately in-memory only, not persisted: a quick manual lever
	// for a session, not a standing config choice (see the web UI's
	// online/offline switch next to the status pill).
	overrideMu       sync.RWMutex
	providerOverride string
	// logger records why a connectivity check ultimately failed (see
	// CheckConnectivity) — nil (the zero value, e.g. a Router built as a
	// struct literal in a test) just means those failures go unlogged,
	// never a panic.
	logger *slog.Logger
}

// SetLogger wires in a logger for otherwise-silent background events —
// currently just a connectivity check that failed after retrying (see
// CheckConnectivity). Optional: without one, those failures simply aren't
// logged, the same "wiring absent means the feature quietly does less"
// pattern used throughout this project.
func (r *Router) SetLogger(logger *slog.Logger) {
	r.logger = logger
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
	remoteRetryBackoff, _ := time.ParseDuration(cfg.Remote.RetryBackoff)
	if remoteRetryBackoff <= 0 {
		remoteRetryBackoff = 500 * time.Millisecond
	}

	var localClient *LocalClient
	var err error
	switch cfg.Local.APIFormat {
	case "", APIFormatOllama:
		localClient = NewLocalClient(cfg.Local.BaseURL, cfg.Local.Model, cfg.Local.Temperature, localTimeout, cfg.Local.Stream)
	case APIFormatOpenAI:
		localClient, err = NewOpenAICompatibleLocalClient(
			cfg.Local.BaseURL,
			cfg.Local.Model,
			cfg.Local.APIKeyEnv,
			cfg.Local.Temperature,
			localTimeout,
			cfg.Local.Stream,
		)
		if err != nil {
			return nil, fmt.Errorf("create local client: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported local API format %q", cfg.Local.APIFormat)
	}
	localClient.supportsTools = cfg.Local.SupportsTools

	remoteClient, err := NewRemoteClient(
		cfg.Remote.BaseURL,
		cfg.Remote.Model,
		cfg.Remote.APIKeyEnv,
		cfg.Remote.Organization,
		cfg.Remote.Temperature,
		remoteTimeout,
	)
	if err != nil {
		// Remote client creation failed (e.g., no API key) - continue with local only
		remoteClient = nil
	}

	return &Router{
		localClient:        localClient,
		remoteClient:       remoteClient,
		config:             cfg,
		isOnline:           false,
		remoteMaxRetries:   max(0, cfg.Remote.MaxRetries),
		remoteRetryBackoff: remoteRetryBackoff,
	}, nil
}

// connectivityCheckAttempts/connectivityCheckRetryDelay: a single failed
// HEAD request used to flip the whole router to "offline" (and every
// request for the next check_interval to the local model) even when the
// cause was a momentary hiccup against that one host rather than a real
// outage — observed directly against this deployment's own remote
// provider, which is known to be occasionally slow (see
// docs/streaming.md's heartbeat section). A real, sustained outage still
// fails every attempt and gets caught just as fast; this only forgives a
// single bad round-trip.
const connectivityCheckAttempts = 3

// connectivityCheckRetryDelay is a var, not a const, so tests can shrink
// it instead of a retry-exhaustion test taking real seconds.
var connectivityCheckRetryDelay = 1 * time.Second

// CheckConnectivity performs a connectivity check against
// router.check_target, retrying a couple of times (see
// connectivityCheckAttempts) before concluding the remote provider is
// actually unreachable.
func (r *Router) CheckConnectivity(ctx context.Context) bool {
	r.checkMu.Lock()
	defer r.checkMu.Unlock()

	checkTimeout, _ := time.ParseDuration(r.config.Router.CheckTimeout)
	if checkTimeout == 0 {
		checkTimeout = 5 * time.Second
	}

	var lastErr error
attempts:
	for attempt := 1; attempt <= connectivityCheckAttempts; attempt++ {
		err := r.probeConnectivityOnce(ctx, checkTimeout)
		if err == nil {
			r.setConnectivity(true)
			return true
		}
		lastErr = err
		if attempt == connectivityCheckAttempts {
			break
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			break attempts
		case <-time.After(connectivityCheckRetryDelay):
		}
	}
	if r.logger != nil {
		r.logger.Warn("remote connectivity check failed after retries",
			"target", r.config.Router.CheckTarget, "attempts", connectivityCheckAttempts, "error", lastErr)
	}
	r.setConnectivity(false)
	return false
}

// probeConnectivityOnce is a single HEAD attempt against check_target — a
// non-nil error (network error, timeout, or a 5xx response, treated the
// same as a transport failure here) means this attempt didn't confirm the
// target reachable.
func (r *Router) probeConnectivityOnce(ctx context.Context, timeout time.Duration) error {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, "HEAD", r.config.Router.CheckTarget, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("check target returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// IsOnline returns the last known connectivity status
func (r *Router) IsOnline() bool {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.isOnline
}

// NetworkAvailable returns a fresh-enough connectivity state. A stale state is
// refreshed synchronously so the agent does not expose online tools after the
// connection has disappeared.
func (r *Router) NetworkAvailable(ctx context.Context) bool {
	checkInterval, _ := time.ParseDuration(r.config.Router.CheckInterval)
	if checkInterval <= 0 {
		checkInterval = 30 * time.Second
	}
	r.stateMu.RLock()
	lastCheck := r.lastCheck
	online := r.isOnline
	r.stateMu.RUnlock()
	if time.Since(lastCheck) <= checkInterval {
		return online
	}
	return r.CheckConnectivity(ctx)
}

// CurrentProvider returns the provider that would serve a request using the
// most recent connectivity state, honoring a manual ProviderOverride first.
func (r *Router) CurrentProvider() string {
	switch r.getProviderOverride() {
	case "local":
		return "local"
	case "remote":
		if r.remoteClient != nil {
			return "remote"
		}
	}
	if r.config.Router.PreferRemote && r.IsOnline() && r.remoteClient != nil {
		return "remote"
	}
	return "local"
}

// ProviderOverride returns the current manual override: "auto" (the
// default — automatic connectivity-based selection), "local", or
// "remote". See SetProviderOverride.
func (r *Router) ProviderOverride() string {
	if override := r.getProviderOverride(); override != "" {
		return override
	}
	return "auto"
}

func (r *Router) getProviderOverride() string {
	r.overrideMu.RLock()
	defer r.overrideMu.RUnlock()
	return r.providerOverride
}

// SetProviderOverride forces every subsequent request onto a specific
// provider, bypassing connectivity checks and prefer_remote entirely —
// "local" never leaves the device even when genuinely online; "remote" is
// a preference, not a guarantee, since Chat/ChatStream still fall back to
// local if the remote request itself fails. "auto" (or "") restores the
// normal automatic selection. Not persisted — resets to auto on restart.
func (r *Router) SetProviderOverride(mode string) error {
	switch mode {
	case "", "auto":
		mode = ""
	case "local", "remote":
		// valid as-is
	default:
		return fmt.Errorf("provider override must be auto, local, or remote, got %q", mode)
	}
	r.overrideMu.Lock()
	r.providerOverride = mode
	r.overrideMu.Unlock()
	return nil
}

// ActiveProvider returns the provider serving an in-flight request, or the
// provider that would be selected for the next request when idle.
func (r *Router) ActiveProvider() string {
	r.stateMu.RLock()
	provider := r.lastProvider
	active := r.providerActive
	r.stateMu.RUnlock()
	if active && provider != "" {
		return provider
	}
	return r.CurrentProvider()
}

// SetTemperatures changes both providers' sampling temperature live (see
// docs/settings.md) — safe to call while requests are in flight.
func (r *Router) SetTemperatures(remote, local float64) {
	if r.remoteClient != nil {
		r.remoteClient.SetTemperature(remote)
	}
	if r.localClient != nil {
		r.localClient.SetTemperature(local)
	}
}

// GetClient returns the appropriate client based on connectivity and
// config, honoring a manual ProviderOverride first.
func (r *Router) GetClient(ctx context.Context) (Client, error) {
	switch r.getProviderOverride() {
	case "local":
		return r.localClient, nil
	case "remote":
		if r.remoteClient != nil {
			return r.remoteClient, nil
		}
		// No remote client configured at all (e.g. missing API key) —
		// fall through to automatic selection rather than erroring on an
		// override that can't actually be honored.
	}

	isOnline := r.NetworkAvailable(ctx)
	preferRemote := r.config.Router.PreferRemote

	if preferRemote && isOnline && r.remoteClient != nil {
		return r.remoteClient, nil
	}

	// Fallback to local
	return r.localClient, nil
}

func (r *Router) setConnectivity(online bool) {
	r.stateMu.Lock()
	r.isOnline = online
	r.lastCheck = time.Now()
	r.stateMu.Unlock()
}

// Chat routes the request to the appropriate provider
func (r *Router) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	client, err := r.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}

	r.setActiveProvider(client.Provider())
	defer r.clearActiveProvider()
	var resp *Response
	if client.Provider() == "remote" {
		resp, err = r.chatRemoteWithRetry(ctx, client, messages, tools)
	} else {
		resp, err = client.Chat(ctx, messages, tools)
	}
	if err != nil {
		// If remote fails and we haven't tried local, try local
		if client.Provider() == "remote" && r.localClient != nil {
			if isNetworkConnectivityError(err) {
				r.setConnectivity(false)
			}
			r.setActiveProvider("local")
			return r.localClient.Chat(ctx, messages, tools)
		}
		return nil, err
	}

	return resp, nil
}

func isNetworkConnectivityError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (r *Router) chatRemoteWithRetry(
	ctx context.Context,
	client Client,
	messages []Message,
	tools []ToolDefinition,
) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt <= r.remoteMaxRetries; attempt++ {
		response, err := client.Chat(ctx, messages, tools)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if attempt == r.remoteMaxRetries || !isRetryableRemoteError(err) {
			break
		}

		delay := r.remoteRetryBackoff * time.Duration(1<<attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

// ChatStream routes a streaming chat request the same way Chat does, with
// one difference: retry and remote→local fallback only apply before any
// delta has reached the caller. Once the user has started seeing text,
// silently retrying or swapping providers mid-stream would be worse than
// just surfacing the error — there's no good way to "un-show" a partial
// answer.
func (r *Router) ChatStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error) {
	client, err := r.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}

	r.setActiveProvider(client.Provider())
	defer r.clearActiveProvider()

	streamer, ok := client.(StreamingClient)
	if !ok {
		// Neither concrete client type should ever hit this — both
		// implement StreamingClient — but degrade gracefully rather than
		// erroring if a future client type doesn't.
		return chatNonStreamingFallback(ctx, client, messages, tools, onDelta)
	}

	started := false
	wrappedOnDelta := func(d StreamDelta) {
		started = true
		if onDelta != nil {
			onDelta(d)
		}
	}

	var resp *Response
	if client.Provider() == "remote" {
		resp, err = r.chatStreamRemoteWithRetry(ctx, streamer, messages, tools, &started, wrappedOnDelta)
	} else {
		resp, err = streamer.ChatStream(ctx, messages, tools, wrappedOnDelta)
	}
	if err != nil {
		if client.Provider() == "remote" && r.localClient != nil && !started {
			if isNetworkConnectivityError(err) {
				r.setConnectivity(false)
			}
			r.setActiveProvider("local")
			return r.localClient.ChatStream(ctx, messages, tools, onDelta)
		}
		return nil, err
	}

	return resp, nil
}

func chatNonStreamingFallback(ctx context.Context, client Client, messages []Message, tools []ToolDefinition, onDelta func(StreamDelta)) (*Response, error) {
	resp, err := client.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	if onDelta != nil && resp.Content != "" {
		onDelta(StreamDelta{Kind: "prose", Text: resp.Content})
	}
	return resp, nil
}

func (r *Router) chatStreamRemoteWithRetry(
	ctx context.Context,
	streamer StreamingClient,
	messages []Message,
	tools []ToolDefinition,
	started *bool,
	onDelta func(StreamDelta),
) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt <= r.remoteMaxRetries; attempt++ {
		response, err := streamer.ChatStream(ctx, messages, tools, onDelta)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if *started || attempt == r.remoteMaxRetries || !isRetryableRemoteError(err) {
			break
		}

		delay := r.remoteRetryBackoff * time.Duration(1<<attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func isRetryableRemoteError(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.statusCode == http.StatusTooManyRequests || statusErr.statusCode >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (r *Router) setActiveProvider(provider string) {
	r.stateMu.Lock()
	r.lastProvider = provider
	r.providerActive = true
	r.stateMu.Unlock()
}

func (r *Router) clearActiveProvider() {
	r.stateMu.Lock()
	r.providerActive = false
	r.stateMu.Unlock()
}

// LocalClient returns the local client for direct access
func (r *Router) LocalClient() *LocalClient {
	return r.localClient
}

// RemoteClient returns the remote client for direct access
func (r *Router) RemoteClient() *RemoteClient {
	return r.remoteClient
}
