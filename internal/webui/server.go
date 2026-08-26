// Package webui serves the LAN-only browser interface for Smart Helper.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/roman220/bosun-smarthelper/internal/adventure"
	"github.com/roman220/bosun-smarthelper/internal/agent"
	"github.com/roman220/bosun-smarthelper/internal/backup"
	"github.com/roman220/bosun-smarthelper/internal/cameras"
	"github.com/roman220/bosun-smarthelper/internal/documents"
	"github.com/roman220/bosun-smarthelper/internal/filedump"
	"github.com/roman220/bosun-smarthelper/internal/metrics"
	"github.com/roman220/bosun-smarthelper/internal/settings"
	"github.com/roman220/bosun-smarthelper/internal/tools"
	"github.com/roman220/bosun-smarthelper/internal/voice"
)

const maxRequestBody = 16 * 1024

// maxDocumentUploadBytes bounds a reference-document upload (e.g. a car
// manual as plain text) — generous compared to maxRequestBody's normal chat
// message cap, but still bounded.
const maxDocumentUploadBytes = 2 << 20

//go:embed index.html
var indexHTML []byte

// staticFS backs index.html's growing set of ES modules (static/shared.js,
// static/cameras.js, ...) — pulled out of the single inline <script> one
// feature at a time, same one-file-per-feature split internal/webui's own
// Go code already uses. fs.Sub strips the "static/" prefix embed.FS keeps
// baked in, so a request for /static/cameras.js resolves to cameras.js
// inside this FS, matching how http.FileServer expects paths.
//
//go:embed static
var rawStaticFS embed.FS

var staticFS = mustSubFS(rawStaticFS, "static")

func mustSubFS(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		// Can't happen: dir is a compile-time embed path, guaranteed to
		// exist by go:embed itself failing the build otherwise.
		panic(err)
	}
	return sub
}

// ValidateBind permits loopback, explicit private LAN addresses, and the
// IPv4/IPv6 wildcard (0.0.0.0, ::) — the wildcard is allowed deliberately so
// a deployment on a DHCP host without a static/reserved lease keeps working
// across an IP change, rather than requiring config.yaml to be hand-edited
// after every reboot. It still rejects public and link-local addresses,
// since this service has no authentication and is meant only for a trusted
// LAN.
func ValidateBind(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid web bind address: %w", err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified()) {
		return fmt.Errorf("web bind host must be localhost, a private IP, or 0.0.0.0")
	}
	return nil
}

// Asker is implemented by agent.Agent.
type Asker interface {
	Ask(ctx context.Context, message string) (string, error)
}

type conversationAsker interface {
	AskWithHistory(ctx context.Context, message string, history []agent.HistoryMessage, language string) (string, error)
}

// streamingConversationAsker is checked separately from conversationAsker
// (not embedded) so a test double implementing only AskWithHistory keeps
// getting today's single-JSON-response behavior unchanged — see
// handleChat.
type streamingConversationAsker interface {
	AskWithHistoryStreaming(
		ctx context.Context,
		message string,
		history []agent.HistoryMessage,
		language string,
		onEvent func(agent.StepEvent),
	) (string, error)
}

// streamEvent is one line of the newline-delimited JSON stream POST
// /api/chat writes when the asker supports streaming. Type is one of
// "step_start", "delta", "done", or "error"; Kind ("prose" or "fold") and
// Text are only set on "delta".
type streamEvent struct {
	Type      string `json:"type"`
	Kind      string `json:"kind,omitempty"`
	Text      string `json:"text,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Position  int    `json:"position,omitempty"`
}

// Status describes the provider that would currently serve a request.
type Status struct {
	Online         bool     `json:"online"`
	Provider       string   `json:"provider"`
	AvailableTools []string `json:"available_tools,omitempty"`
}

// Server serves the embedded UI and chat API.
type Server struct {
	asker             Asker
	status            func() Status
	logger            *slog.Logger
	requestTimeout    time.Duration
	local             localQueue
	sessionsMu        sync.Mutex
	sessions          map[string]chatSession
	sessionOptions    SessionOptions
	documents         *documents.Store
	documentImagesDir string
	activityMu        sync.Mutex
	lastChatAt        time.Time
	languageMu        sync.RWMutex
	defaultLanguage   string
	settingsStore     *settings.Store
	temps             temperatureController
	caCertFile        string
	ttsEngine         voice.TTSEngine
	sttEngine         voice.STTEngine
	providerOverride  providerOverrideController
	metricsStore      *metrics.Store
	metricsLabels     map[string]MetricLabel
	memoTool          *tools.MemoTool
	toolRegistry      *tools.Registry
	backupS3Cfg       *backup.S3Config
	backupDataDir     string
	alertsConfigured  alertsConfigured
	alertsTestSender  func(ctx context.Context, channel string) error
	cameraManager     *cameras.Manager
	cameraDataDir     string
	generationsMu     sync.Mutex
	generations       map[string]*generationHandle

	adventureStore         *adventure.Store
	adventureNarrator      adventureNarrator
	adventureNarrateLocal  bool
	adventureNarrateRemote bool
	adventureMediaDir      string

	fileDumpStore *filedump.Store
	fileDumpDir   string
}

// generationHandle is the in-flight state for one session's chat request —
// tracked so a generation survives the client going away (a page reload,
// most commonly) instead of being cancelled by it, while still leaving a
// deliberate stop (POST /api/chat/stop) able to cancel exactly this
// generation and no other. A pointer, not the bare context.CancelFunc,
// because Go doesn't allow comparing func values — completion needs to
// check "is this still the generation I registered" before deleting the
// map entry, in case a second request for the same session_id has since
// registered its own. See docs/streaming.md.
type generationHandle struct {
	cancel context.CancelFunc
}

// alertsConfigured tracks which alert channels config.yaml/.env actually
// set up, so the settings page only offers a toggle for a channel that
// would do something if enabled — see SetAlertsConfigured.
type alertsConfigured struct {
	Telegram bool
	Webhook  bool
	Speaker  bool
}

// SetDefaultLanguage changes the language used when a chat request doesn't
// specify one — e.g. from the settings page (see docs/settings.md), live,
// no restart. Ignored if lang isn't "ru" or "en".
func (s *Server) SetDefaultLanguage(lang string) {
	if lang != "ru" && lang != "en" {
		return
	}
	s.languageMu.Lock()
	s.defaultLanguage = lang
	s.languageMu.Unlock()
}

func (s *Server) getDefaultLanguage() string {
	s.languageMu.RLock()
	defer s.languageMu.RUnlock()
	return s.defaultLanguage
}

// markChatActivity records that a chat request just finished — see
// TryIdleAfter. Zero value (never called) reads as "idle forever," so
// background work is allowed to run immediately after a fresh start with
// no chat history yet.
func (s *Server) markChatActivity() {
	s.activityMu.Lock()
	s.lastChatAt = time.Now()
	s.activityMu.Unlock()
}

// TryIdleAfter runs fn only if at least quiet has passed since the last
// chat request finished, and — only when Status.Provider is "local" (not
// merely whether the internet is reachable; a reachable-but-not-preferred
// remote still means Provider is "local" — see Router.CurrentProvider) —
// no local generation is in flight either (claiming the same slot
// handleChat's local path would, so the two never run concurrently on
// this host's shared, weak hardware). A remote-served chat has no such
// shared-hardware contention with a background LLM call, so in that case
// only the quiet-period check applies. Returns false without running fn if
// a condition isn't met — the caller is expected to just try again later
// (e.g. background memo tag normalization; see docs/memo-search.md).
//
// The quiet-period check matters on top of the slot check alone: without
// it, a tick landing moments after a chat just ended would immediately
// claim the slot for up to fn's own duration, so a user typing a follow-up
// right then would queue behind background maintenance instead of getting
// an instant reply.
func (s *Server) TryIdleAfter(quiet time.Duration, fn func()) bool {
	s.activityMu.Lock()
	lastChatAt := s.lastChatAt
	s.activityMu.Unlock()
	if !lastChatAt.IsZero() && time.Since(lastChatAt) < quiet {
		return false
	}

	if s.status().Provider != "local" {
		fn()
		return true
	}

	if !s.local.tryHold() {
		return false
	}
	defer s.local.release()
	fn()
	return true
}

// SetDocumentStore wires in reference-document upload/search — see
// docs/memo-search.md. Optional: nil (the default) means the
// /api/documents endpoints report the feature as disabled rather than
// erroring, and the memo tool's "search" only ever considers memos.
func (s *Server) SetDocumentStore(store *documents.Store) {
	s.documents = store
	if store != nil {
		s.documentImagesDir = store.ImagesDir()
	}
}

type chatSession struct {
	History   []agent.HistoryMessage
	UpdatedAt time.Time

	// AdventureMode/AdventureSessionName: while AdventureMode is set,
	// every message in this conversation goes straight to the named
	// go-adventure session instead of the LLM/tool-calling loop — see
	// docs/adventure.md and internal/webui/adventure.go.
	AdventureMode        bool
	AdventureSessionName string
}

// HistoryBudget caps how much conversation history is kept for one provider.
type HistoryBudget struct {
	Turns    int
	MaxChars int
}

// SessionOptions bounds in-memory web conversation history. Local and Remote
// are separate budgets: a weak local fallback model needs a small window,
// while a remote model's context is comparatively unlimited. Which one
// trims a given request is decided at request time from current
// connectivity (see handleChat) — Remote also acts as the storage cap, so
// history isn't discarded before an online turn gets a chance to use it.
type SessionOptions struct {
	Local       HistoryBudget
	Remote      HistoryBudget
	TTL         time.Duration
	MaxSessions int
	// StorePath persists sessions to disk so chat history survives a page
	// reload and a service restart. Empty disables persistence (in-memory
	// only, the prior behavior).
	StorePath string
}

// NewServer creates a single-user web server backed by the existing agent.
func NewServer(
	asker Asker,
	status func() Status,
	requestTimeout time.Duration,
	defaultLanguage string,
	logger *slog.Logger,
	sessionOptions ...SessionOptions,
) *Server {
	if requestTimeout <= 0 {
		requestTimeout = 3 * time.Minute
	}
	if defaultLanguage != "en" {
		defaultLanguage = "ru"
	}
	if logger == nil {
		logger = slog.Default()
	}
	if status == nil {
		status = func() Status { return Status{Provider: "local"} }
	}
	options := SessionOptions{
		Local:       HistoryBudget{Turns: 4, MaxChars: 4000},
		Remote:      HistoryBudget{Turns: 40, MaxChars: 60000},
		TTL:         24 * time.Hour,
		MaxSessions: 100,
	}
	if len(sessionOptions) > 0 {
		options = sessionOptions[0]
	}
	if options.Local.Turns <= 0 {
		options.Local.Turns = 4
	}
	if options.Local.MaxChars <= 0 {
		options.Local.MaxChars = 4000
	}
	if options.Remote.Turns <= 0 {
		options.Remote.Turns = 40
	}
	if options.Remote.MaxChars <= 0 {
		options.Remote.MaxChars = 60000
	}
	if options.Remote.Turns < options.Local.Turns {
		options.Remote.Turns = options.Local.Turns
	}
	if options.Remote.MaxChars < options.Local.MaxChars {
		options.Remote.MaxChars = options.Local.MaxChars
	}
	if options.TTL <= 0 {
		options.TTL = 24 * time.Hour
	}
	if options.MaxSessions <= 0 {
		options.MaxSessions = 100
	}

	sessions := make(map[string]chatSession)
	if options.StorePath != "" {
		loaded, err := loadSessionStore(options.StorePath)
		if err != nil {
			logger.Warn("could not load persisted chat sessions; starting empty", "error", err)
		} else {
			sessions = loaded
		}
	}

	return &Server{
		asker:           asker,
		status:          status,
		logger:          logger,
		requestTimeout:  requestTimeout,
		defaultLanguage: defaultLanguage,
		sessions:        sessions,
		sessionOptions:  options,
		generations:     make(map[string]*generationHandle),
	}
}

// beginGeneration registers the cancel func for a session's in-flight
// generation, so a later POST /api/chat/stop can find and cancel exactly
// this one. Returns a func to call when the generation ends (success,
// failure, or its own timeout) that removes the registration — but only if
// it's still the same handle, in case a second request for the same
// session_id has since replaced it.
func (s *Server) beginGeneration(sessionID string, cancel context.CancelFunc) func() {
	handle := &generationHandle{cancel: cancel}
	s.generationsMu.Lock()
	s.generations[sessionID] = handle
	s.generationsMu.Unlock()
	return func() {
		s.generationsMu.Lock()
		if s.generations[sessionID] == handle {
			delete(s.generations, sessionID)
		}
		s.generationsMu.Unlock()
	}
}

type chatStopRequest struct {
	SessionID string `json:"session_id"`
}

// handleChatStop cancels a session's in-flight generation, if any — the
// only way one actually gets cancelled now that it no longer dies with the
// client's own connection (see beginGeneration and docs/streaming.md).
// Reports "stopped": false rather than an error when there's nothing to
// cancel; by the time this arrives the generation may well have already
// finished on its own, which isn't a failure worth surfacing.
func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request chatStopRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !validSessionID(request.SessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session_id"})
		return
	}
	s.generationsMu.Lock()
	handle := s.generations[request.SessionID]
	s.generationsMu.Unlock()
	if handle != nil {
		handle.cancel()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": handle != nil})
}

// Handler returns the complete HTTP handler for the web UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/chat/stop", s.handleChatStop)
	mux.HandleFunc("POST /api/adventure/mode", s.handleAdventureMode)
	mux.HandleFunc("GET /api/adventure/sessions", s.handleAdventureSessionsList)
	mux.HandleFunc("POST /api/adventure/sessions", s.handleAdventureSessionCreate)
	mux.HandleFunc("PATCH /api/adventure/sessions/{name}", s.handleAdventureSessionRename)
	mux.HandleFunc("DELETE /api/adventure/sessions/{name}", s.handleAdventureSessionDelete)
	mux.HandleFunc("POST /api/session/clear", s.handleSessionClear)
	mux.HandleFunc("GET /api/documents", s.handleDocumentsList)
	mux.HandleFunc("POST /api/documents/pages", s.handleDocumentAddPages)
	mux.HandleFunc("DELETE /api/documents/{id}", s.handleDocumentDelete)
	mux.HandleFunc("GET /api/files", s.handleFileDumpList)
	mux.HandleFunc("POST /api/files/folder", s.handleFileDumpFolder)
	mux.HandleFunc("POST /api/files/upload", s.handleFileDumpUpload)
	mux.HandleFunc("POST /api/files/move", s.handleFileDumpMove)
	mux.HandleFunc("DELETE /api/files", s.handleFileDumpDelete)
	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("POST /api/settings", s.handleSettingsUpdate)
	mux.HandleFunc("GET /ca.pem", s.handleCACert)
	mux.HandleFunc("POST /api/tts", s.handleTTS)
	mux.HandleFunc("POST /api/stt", s.handleSTT)
	mux.HandleFunc("POST /api/feedback", s.handleFeedback)
	mux.HandleFunc("POST /api/client-error", s.handleClientError)
	mux.HandleFunc("POST /api/provider-override", s.handleProviderOverride)
	mux.HandleFunc("GET /api/metrics/list", s.handleMetricsList)
	mux.HandleFunc("GET /api/metrics", s.handleMetricsQuery)
	mux.HandleFunc("POST /api/alerts/test", s.handleAlertsTest)
	mux.HandleFunc("GET /api/metric-merges", s.handleMetricMergesList)
	mux.HandleFunc("POST /api/metric-merges/{id}/decide", s.handleMetricMergeDecide)
	mux.HandleFunc("GET /api/quick/{tool}", s.handleQuickTool)
	mux.HandleFunc("GET /api/backups", s.handleBackupsList)
	mux.HandleFunc("POST /api/backups", s.handleBackupRun)
	mux.HandleFunc("GET /api/cameras/list", s.handleCamerasList)
	mux.HandleFunc("GET /api/cameras/{name}/stream", s.handleCameraStream)
	mux.HandleFunc("GET /api/cameras/{name}/archive", s.handleCameraArchiveList)
	mux.HandleFunc("GET /api/cameras/{name}/archive/{file}", s.handleCameraArchiveFile)
	if s.documentImagesDir != "" {
		mux.Handle("GET /document-images/", http.StripPrefix("/document-images/", http.FileServer(http.Dir(s.documentImagesDir))))
	}
	if s.adventureMediaDir != "" {
		// Registered as its own, more specific pattern so ServeMux routes
		// it here instead of the embed.FS-backed "/static/" handler above
		// — the generated art/audio is real (~350MB), deliberately never
		// committed to git or baked into the binary (see docs/adventure.md),
		// just a plain host directory bind-mounted in, same reasoning as
		// documentImagesDir above.
		mux.Handle("GET /static/adventure/", http.StripPrefix("/static/adventure/", http.FileServer(http.Dir(s.adventureMediaDir))))
	}
	if s.fileDumpDir != "" {
		// Same reasoning as documentImagesDir/adventureMediaDir above: a
		// plain host directory served as-is, registered as a more specific
		// pattern than the embedded "/static/" handler.
		mux.Handle("GET /files/", http.StripPrefix("/files/", http.FileServer(http.Dir(s.fileDumpDir))))
	}
	return securityHeaders(mux)
}

func newHTTPServer(address string, handler http.Handler, requestTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      requestTimeout + 15*time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// Serve listens until ctx is cancelled. When both certFile and keyFile are
// non-empty it serves HTTPS — an mkcert-issued cert (docs/tls.md) or a real
// publicly-trusted one for a domain name (docs/cloudflare.md), Serve itself
// doesn't care which; otherwise plain HTTP, matching every deployment
// before TLS support existed. When TLS is enabled and httpFallbackBind is
// non-empty, it additionally serves plain HTTP (same handler) on that
// address — for a device that can't be made to trust the TLS cert at all
// (e.g. a corporate MDM-managed phone that blocks installing custom root
// certs).
func (s *Server) Serve(ctx context.Context, address, certFile, keyFile, httpFallbackBind string) error {
	handler := s.Handler()
	useTLS := certFile != "" && keyFile != ""

	primary := newHTTPServer(address, handler, s.requestTimeout)
	servers := []*http.Server{primary}

	var fallback *http.Server
	if useTLS && httpFallbackBind != "" {
		fallback = newHTTPServer(httpFallbackBind, handler, s.requestTimeout)
		servers = append(servers, fallback)
	}

	errCh := make(chan error, len(servers))
	go func() {
		if useTLS {
			errCh <- primary.ListenAndServeTLS(certFile, keyFile)
		} else {
			errCh <- primary.ListenAndServe()
		}
	}()
	if fallback != nil {
		go func() { errCh <- fallback.ListenAndServe() }()
	}

	// A single chat request can legitimately run for minutes (slow local
	// hardware) — far longer than a restart should wait. Give it a short
	// grace period, then abandon in-flight requests rather than treat a
	// timeout here as a failure; the process is exiting either way.
	shutdownAll := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, srv := range servers {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				s.logger.Warn("graceful shutdown timed out; abandoning in-flight requests", "address", srv.Addr, "error", err)
			}
		}
	}

	select {
	case <-ctx.Done():
		shutdownAll()
		return nil
	case err := <-errCh:
		shutdownAll()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve web interface: %w", err)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	status := s.status()
	response := map[string]any{
		"online":          status.Online,
		"provider":        status.Provider,
		"available_tools": status.AvailableTools,
		// So the web UI can hide the mic button entirely when there's no
		// STT engine configured, instead of a dead-end recording flow.
		"stt_enabled": s.sttEngine != nil,
		// Same idea for the monitoring dashboard button (docs/monitoring.md).
		"metrics_enabled": s.metricsStore != nil,
	}
	if s.providerOverride != nil {
		response["provider_override"] = s.providerOverride.ProviderOverride()
	}
	writeJSON(w, http.StatusOK, response)
}

// handleHistory returns a session's stored transcript so the client can
// repopulate the visible chat log after a page reload. An unknown or
// missing session_id yields an empty list rather than an error — this is a
// hydration nicety, not something worth surfacing as a failure.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	messages := []agent.HistoryMessage{}
	if validSessionID(sessionID) {
		if history := s.loadHistory(sessionID); history != nil {
			messages = history
		}
	}
	response := map[string]any{"messages": messages}
	// Game mode (docs/adventure.md) is tracked server-side per session_id,
	// but the header toggle's highlight is purely client-side JS state —
	// without this, a page reload left the button looking off even though
	// the conversation was still actually in game mode underneath.
	if adventureSessionName, ok := s.adventureModeSession(sessionID); ok {
		response["adventure_mode"] = true
		response["adventure_session"] = adventureSessionName
	}
	writeJSON(w, http.StatusOK, response)
}

type chatRequest struct {
	Message   string `json:"message"`
	Language  string `json:"language"`
	SessionID string `json:"session_id,omitempty"`
}

type chatResponse struct {
	Answer    string `json:"answer,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`

	// LocationID is set only by a game-mode turn (internal/webui/adventure.go)
	// that actually moved the player to a new location — never on every
	// turn — so the frontend knows exactly when to swap the location's
	// art/ambient audio rather than re-fetching it on every message.
	LocationID *int32 `json:"location_id,omitempty"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var request chatRequest
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "invalid request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "invalid request"})
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "message is required"})
		return
	}
	if len([]rune(request.Message)) > 4000 {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "message is too long"})
		return
	}

	language := request.Language
	if language == "" {
		language = s.getDefaultLanguage()
	}
	if language != "ru" && language != "en" {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "unsupported language"})
		return
	}
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" {
		var err error
		sessionID, err = newSessionID()
		if err != nil {
			s.logger.Error("create chat session", "error", err)
			writeJSON(w, http.StatusInternalServerError, chatResponse{Error: "could not create session"})
			return
		}
	}
	if !validSessionID(sessionID) {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "invalid session_id"})
		return
	}

	// Deliberately NOT derived from r.Context(): tying generation to the
	// request's own connection meant a page reload — not just an explicit
	// stop — silently killed the model call mid-answer, discarding work
	// a slow remote generation may have spent a long time on (see
	// docs/streaming.md). beginGeneration below is what makes an
	// intentional stop (POST /api/chat/stop) still work despite this.
	ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout)
	defer cancel()
	endGeneration := s.beginGeneration(sessionID, cancel)
	defer endGeneration()
	// Scopes a run_code workspace (internal/tools.CodeExecTool,
	// docs/sandbox.md) to this real chat session — never something the
	// LLM itself supplies, since sessionID here is already
	// server-generated/validated above.
	ctx = tools.ContextWithSessionID(ctx, sessionID)

	// Game mode (docs/adventure.md): every message in this conversation
	// goes straight to the named go-adventure session, bypassing the
	// LLM/tool-calling loop (and its local-model queueing/streaming
	// machinery below) entirely — deliberately simpler than the general
	// chat path, since a game turn is normally instant and the optional
	// narration step (internal/webui/adventure.go) is always exactly one
	// plain call, never something that needs to stream or queue.
	if adventureSessionName, ok := s.adventureModeSession(sessionID); ok {
		s.handleAdventureTurn(w, ctx, sessionID, request.Message, adventureSessionName, request.Language)
		return
	}

	// Only the local model needs serializing — it's weak, shared hardware
	// that can't usefully run more than one generation at a time. The
	// remote provider handles concurrent requests fine on its own, so a
	// remote-served request never queues at all. Gate on Provider, not
	// Online: Online only reflects internet reachability, whereas Provider
	// accounts for llm.router.prefer_remote too — with prefer_remote
	// false, Provider is "local" even while genuinely online. See
	// docs/streaming.md.
	streamer, supportsStreaming := s.asker.(streamingConversationAsker)
	servedLocally := s.status().Provider == "local"
	var write func(streamEvent)
	if servedLocally {
		turn, position := s.local.join()
		if position > 0 {
			if supportsStreaming {
				// Tell the client immediately instead of leaving it to wait
				// silently — see index.html's handling of "queued".
				write = s.newNDJSONWriter(w)
				write(streamEvent{Type: "queued", Position: position})
			}
			s.logger.Info("web chat queued behind the local model", "position", position)
		}
		select {
		case <-turn:
			defer s.local.release()
			defer s.markChatActivity()
		case <-ctx.Done():
			s.local.abandon(turn)
			if write != nil {
				// Already committed to the ndjson stream via the queued
				// event above — status 200 is locked in, so the failure
				// has to be an event, not an HTTP status.
				write(streamEvent{Type: "error", Message: "assistant is busy"})
			} else {
				writeJSON(w, http.StatusGatewayTimeout, chatResponse{Error: "assistant is busy"})
			}
			return
		}
	}

	history := s.loadHistory(sessionID)
	// Saved now, not after the answer comes back — see saveUserMessage.
	s.saveUserMessage(sessionID, request.Message)
	if servedLocally {
		// The local model is about to serve this request: trim to its small
		// budget for the outgoing call only. The full history stays in the
		// session (bounded by the larger Remote budget in saveTurn) so a
		// later online turn isn't missing context it never actually lost.
		history = trimHistory(history, s.sessionOptions.Local.Turns, s.sessionOptions.Local.MaxChars)
	}
	if supportsStreaming {
		s.handleChatStreaming(w, ctx, streamer, sessionID, request.Message, history, language, write)
		return
	}

	var answer string
	var err error
	if conversational, ok := s.asker.(conversationAsker); ok {
		answer, err = conversational.AskWithHistory(ctx, request.Message, history, language)
	} else {
		answer, err = s.asker.Ask(ctx, request.Message)
	}
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			// The client (e.g. the "stop" button) cancelled the request —
			// routine, not a failure worth an ERROR log line.
			s.logger.Info("web chat cancelled by client")
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			s.logger.Warn("web chat timed out", "error", err)
			status = http.StatusGatewayTimeout
		default:
			s.logger.Error("web chat failed", "error", err)
		}
		writeJSON(w, status, chatResponse{Error: "assistant request failed"})
		return
	}
	s.saveAssistantReply(sessionID, answer)
	writeJSON(w, http.StatusOK, chatResponse{Answer: answer, SessionID: sessionID})
}

// newNDJSONWriter sets the response up for newline-delimited JSON events
// and returns a function that encodes and flushes one. Exposed separately
// from handleChatStreaming so handleChat can emit a "queued" event (see
// docs/streaming.md) before the asker call even starts, then hand the same
// writer into handleChatStreaming instead of it creating a second one.
func (s *Server) newNDJSONWriter(w http.ResponseWriter) func(streamEvent) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	var mu sync.Mutex
	return func(event streamEvent) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(event)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// heartbeatInterval is how long handleChatStreaming lets the connection sit
// idle (no real event written) before sending a "ping" line. It exists
// because a remote generation can legitimately stall for a while
// mid-answer (see docs/streaming.md), and unlike bosun's own
// web.request_timeout, an intermediary like Cloudflare's tunnel enforces
// its own, shorter, non-configurable idle-between-chunks timeout — one
// that has nothing to do with total request duration and would otherwise
// kill the connection while bosun is still working. index.html's NDJSON
// parser already ignores unrecognized event types, so this needs no
// frontend change. Comfortably below a Cloudflare tunnel's ~100s idle
// budget, and comfortably above normal per-token latency so it never
// fires during healthy fast generation. A var, not a const, so tests can
// shrink it instead of running real-time for 15s.
var heartbeatInterval = 15 * time.Second

// handleChatStreaming writes one JSON object per line as the answer is
// generated. Once the first line is flushed the HTTP status is locked in as
// 200 by Go's ResponseWriter — success/failure is communicated by the
// event's "type" ("done" vs "error"), not the HTTP status, since there's no
// way to change the status after streaming has started. write may already
// be set (a "queued" event was sent before this was called) — nil means
// create a fresh one.
func (s *Server) handleChatStreaming(
	w http.ResponseWriter,
	ctx context.Context,
	asker streamingConversationAsker,
	sessionID, message string,
	history []agent.HistoryMessage,
	language string,
	write func(streamEvent),
) {
	if write == nil {
		write = s.newNDJSONWriter(w)
	}

	var lastActivityMu sync.Mutex
	lastActivity := time.Now()
	touch := func() {
		lastActivityMu.Lock()
		lastActivity = time.Now()
		lastActivityMu.Unlock()
	}

	// Captured here, synchronously, rather than read from inside the
	// goroutine below: heartbeatInterval is a var (not a const) so tests
	// can shrink it, and a stray goroutine from a finished test reading
	// the package var directly could otherwise race a later test's write
	// to it.
	interval := heartbeatInterval

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	var heartbeatDone sync.WaitGroup
	heartbeatDone.Add(1)
	go func() {
		defer heartbeatDone.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				lastActivityMu.Lock()
				idle := time.Since(lastActivity)
				lastActivityMu.Unlock()
				if idle >= interval {
					write(streamEvent{Type: "ping"})
				}
			}
		}
	}()
	// Cancelling alone only *signals* the goroutine above to stop — it can
	// still be mid-write() when this function returns, racing whatever
	// happens to the response next (a real net/http.Server just drops that
	// stray write on an already-closed connection, harmless, but a test
	// reading the recorded body right after ServeHTTP returns doesn't get
	// that protection). Waiting here closes that window for good.
	defer func() {
		cancelHeartbeat()
		heartbeatDone.Wait()
	}()

	answer, err := asker.AskWithHistoryStreaming(ctx, message, history, language, func(e agent.StepEvent) {
		touch()
		switch e.Type {
		case "step_start":
			write(streamEvent{Type: "step_start"})
		case "delta":
			write(streamEvent{Type: "delta", Kind: e.Delta.Kind, Text: e.Delta.Text})
		}
	})
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			s.logger.Info("web chat cancelled by client")
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			s.logger.Warn("web chat timed out", "error", err)
		default:
			s.logger.Error("web chat failed", "error", err)
		}
		write(streamEvent{Type: "error", Message: "assistant request failed"})
		return
	}

	s.saveAssistantReply(sessionID, answer)
	write(streamEvent{Type: "done", SessionID: sessionID})
}

type clearSessionRequest struct {
	SessionID string `json:"session_id"`
}

func (s *Server) handleSessionClear(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var request clearSessionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || !validSessionID(request.SessionID) {
		writeJSON(w, http.StatusBadRequest, chatResponse{Error: "invalid session_id"})
		return
	}
	s.sessionsMu.Lock()
	delete(s.sessions, request.SessionID)
	s.persistLocked()
	s.sessionsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// handleDocumentsList reports "enabled": false rather than an error when no
// document store is configured — the UI uses that to hide the upload form
// instead of showing a broken feature.
func (s *Server) handleDocumentsList(w http.ResponseWriter, r *http.Request) {
	if s.documents == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "documents": []any{}})
		return
	}
	list, err := s.documents.List()
	if err != nil {
		s.logger.Error("list documents", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list documents"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "documents": list})
}

// pdfMagic is the standard PDF file signature.
var pdfMagic = []byte("%PDF-")

type addPagesRequest struct {
	Title string `json:"title"`
	Pages []struct {
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	} `json:"pages"`
}

// handleDocumentAddPages is a scripted/advanced ingestion path (no UI
// button) for callers that already have pre-segmented pages with their own
// image references — e.g. a bulk import script that extracted a source
// site's HTML pages and copied its diagram images into documentImagesDir
// itself. Ordinary uploads go through handleFileDumpUpload instead (see
// filedump.go), with add_to_rag=true.
func (s *Server) handleDocumentAddPages(w http.ResponseWriter, r *http.Request) {
	if s.documents == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "document search is not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentUploadBytes)
	var request addPagesRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if len(request.Pages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pages is required"})
		return
	}
	pages := make([]documents.PageInput, len(request.Pages))
	for i, p := range request.Pages {
		pages[i] = documents.PageInput{Text: p.Text, ImageURL: p.ImageURL}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	summary, err := s.documents.AddPages(ctx, request.Title, pages, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleDocumentDelete(w http.ResponseWriter, r *http.Request) {
	if s.documents == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "document search is not configured"})
		return
	}
	if err := s.documents.Delete(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func newSessionID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validSessionID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) loadHistory(sessionID string) []agent.HistoryMessage {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.purgeExpiredLocked(time.Now())
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	return append([]agent.HistoryMessage(nil), session.History...)
}

// saveUserMessage persists the user's half of a turn immediately, before
// the asker call even starts. A generation can legitimately run for a long
// time (see docs/streaming.md's heartbeat section); without this, a page
// refresh mid-generation lost the question along with the never-saved
// answer, since nothing reached disk until the whole turn completed.
// saveAssistantReply appends the other half once (if) an answer arrives —
// on failure or an abandoned request, the user message is left as the
// last, unanswered entry, which is an honest reflection of what actually
// happened and renders fine (hydrateHistory in index.html tolerates a
// trailing user message with no reply).
func (s *Server) saveUserMessage(sessionID, userMessage string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	now := time.Now()
	s.purgeExpiredLocked(now)
	if _, exists := s.sessions[sessionID]; !exists && len(s.sessions) >= s.sessionOptions.MaxSessions {
		s.evictOldestLocked()
	}
	session := s.sessions[sessionID]
	session.History = append(session.History, agent.HistoryMessage{Role: "user", Content: userMessage})
	session.History = trimHistory(session.History, s.sessionOptions.Remote.Turns, s.sessionOptions.Remote.MaxChars)
	session.UpdatedAt = now
	s.sessions[sessionID] = session
	s.persistLocked()
}

func (s *Server) saveAssistantReply(sessionID, answer string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	now := time.Now()
	s.purgeExpiredLocked(now)
	session := s.sessions[sessionID]
	session.History = append(session.History, agent.HistoryMessage{Role: "assistant", Content: answer})
	session.History = trimHistory(session.History, s.sessionOptions.Remote.Turns, s.sessionOptions.Remote.MaxChars)
	session.UpdatedAt = now
	s.sessions[sessionID] = session
	s.persistLocked()
}

// adventureModeSession reports whether sessionID's conversation is
// currently in game mode and, if so, which named go-adventure session
// it's pointed at.
func (s *Server) adventureModeSession(sessionID string) (string, bool) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok || !session.AdventureMode {
		return "", false
	}
	return session.AdventureSessionName, true
}

// setAdventureMode flips sessionID's conversation into or out of game
// mode. Selecting which named session becomes active is a plain,
// LLM-free write — deliberately, so it works the same with or without a
// model available (see docs/adventure.md).
func (s *Server) setAdventureMode(sessionID string, enabled bool, adventureSessionName string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	now := time.Now()
	s.purgeExpiredLocked(now)
	if _, exists := s.sessions[sessionID]; !exists && len(s.sessions) >= s.sessionOptions.MaxSessions {
		s.evictOldestLocked()
	}
	session := s.sessions[sessionID]
	session.AdventureMode = enabled
	session.AdventureSessionName = adventureSessionName
	session.UpdatedAt = now
	s.sessions[sessionID] = session
	s.persistLocked()
}

func (s *Server) purgeExpiredLocked(now time.Time) {
	for id, session := range s.sessions {
		if now.Sub(session.UpdatedAt) > s.sessionOptions.TTL {
			delete(s.sessions, id)
		}
	}
}

func (s *Server) evictOldestLocked() {
	var oldestID string
	var oldestTime time.Time
	for id, session := range s.sessions {
		if oldestID == "" || session.UpdatedAt.Before(oldestTime) {
			oldestID = id
			oldestTime = session.UpdatedAt
		}
	}
	delete(s.sessions, oldestID)
}

// sessionStoreFile is the on-disk shape written to SessionOptions.StorePath.
type sessionStoreFile struct {
	Sessions map[string]chatSession `json:"sessions"`
}

// DefaultSessionStorePath mirrors the memo/error-log convention
// (~/.local/share/bosun/...). NewServer does NOT resolve an empty StorePath
// to this by itself — empty means "no persistence" at the library level, so
// tests and other callers that don't set it never touch the real
// filesystem. Application wiring (cmd/smarthelper) calls this explicitly.
func DefaultSessionStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "sessions.json"
	}
	return filepath.Join(home, ".local", "share", "bosun", "sessions.json")
}

// loadSessionStore reads a persisted session store. A missing file yields an
// empty, non-nil map and no error.
func loadSessionStore(path string) (map[string]chatSession, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return make(map[string]chatSession), nil
	}
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	defer file.Close()

	var data sessionStoreFile
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode session store: %w", err)
	}
	if data.Sessions == nil {
		data.Sessions = make(map[string]chatSession)
	}
	return data.Sessions, nil
}

// persistLocked writes the current session map to disk. Caller must hold
// s.sessionsMu. A failure is logged, not returned — chat keeps working
// in-memory even if the disk write fails.
func (s *Server) persistLocked() {
	if s.sessionOptions.StorePath == "" {
		return
	}
	snapshot := make(map[string]chatSession, len(s.sessions))
	for id, session := range s.sessions {
		snapshot[id] = session
	}
	if err := writeSessionStore(s.sessionOptions.StorePath, snapshot); err != nil {
		s.logger.Warn("persist chat sessions", "error", err)
	}
}

// writeSessionStore atomically replaces path's contents (temp file + rename),
// matching the memo store's durability pattern.
func writeSessionStore(path string, sessions map[string]chatSession) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create session store directory: %w", err)
	}
	payload, err := json.Marshal(sessionStoreFile{Sessions: sessions})
	if err != nil {
		return fmt.Errorf("encode session store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary session store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set session store permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write session store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync session store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session store: %w", err)
	}
	return os.Rename(temporaryPath, path)
}

func trimHistory(history []agent.HistoryMessage, maxTurns, maxChars int) []agent.HistoryMessage {
	maxMessages := maxTurns * 2
	if len(history) > maxMessages {
		history = history[len(history)-maxMessages:]
	}
	for historyChars(history) > maxChars && len(history) >= 2 {
		history = history[2:]
	}
	return append([]agent.HistoryMessage(nil), history...)
}

func historyChars(history []agent.HistoryMessage) int {
	total := 0
	for _, message := range history {
		total += utf8.RuneCountInString(message.Content)
	}
	return total
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; media-src 'self' data: blob:")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
