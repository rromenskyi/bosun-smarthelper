// Package webui serves the LAN-only browser interface for Smart Helper.
package webui

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/roman220/ai-local-smarthelper/internal/agent"
)

const maxRequestBody = 16 * 1024

//go:embed index.html
var indexHTML []byte

// ValidateBind permits only loopback or explicit private LAN addresses.
func ValidateBind(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid web bind address: %w", err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return fmt.Errorf("web bind host must be localhost or a private IP")
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

// Status describes the provider that would currently serve a request.
type Status struct {
	Online         bool     `json:"online"`
	Provider       string   `json:"provider"`
	AvailableTools []string `json:"available_tools,omitempty"`
}

// Server serves the embedded UI and chat API.
type Server struct {
	asker           Asker
	status          func() Status
	logger          *slog.Logger
	requestTimeout  time.Duration
	defaultLanguage string
	chatSlot        chan struct{}
	sessionsMu      sync.Mutex
	sessions        map[string]chatSession
	sessionOptions  SessionOptions
}

type chatSession struct {
	History   []agent.HistoryMessage
	UpdatedAt time.Time
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
		chatSlot:        make(chan struct{}, 1),
		sessions:        sessions,
		sessionOptions:  options,
	}
}

// Handler returns the complete HTTP handler for the web UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/session/clear", s.handleSessionClear)
	return securityHeaders(mux)
}

// Serve listens until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, address string) error {
	server := &http.Server{
		Addr:              address,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      s.requestTimeout + 15*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		// A single chat request can legitimately run for minutes (slow local
		// hardware) — far longer than a restart should wait. Give it a short
		// grace period, then abandon in-flight requests rather than treat a
		// timeout here as a failure; the process is exiting either way.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("graceful shutdown timed out; abandoning in-flight requests", "error", err)
		}
		return nil
	case err := <-errCh:
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
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.status())
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
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
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
		language = s.defaultLanguage
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

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	select {
	case s.chatSlot <- struct{}{}:
		defer func() { <-s.chatSlot }()
	case <-ctx.Done():
		writeJSON(w, http.StatusGatewayTimeout, chatResponse{Error: "assistant is busy"})
		return
	}

	history := s.loadHistory(sessionID)
	if !s.status().Online {
		// The local model is about to serve this request: trim to its small
		// budget for the outgoing call only. The full history stays in the
		// session (bounded by the larger Remote budget in saveTurn) so a
		// later online turn isn't missing context it never actually lost.
		history = trimHistory(history, s.sessionOptions.Local.Turns, s.sessionOptions.Local.MaxChars)
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
	s.saveTurn(sessionID, request.Message, answer)
	writeJSON(w, http.StatusOK, chatResponse{Answer: answer, SessionID: sessionID})
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

func (s *Server) saveTurn(sessionID, userMessage, answer string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	now := time.Now()
	s.purgeExpiredLocked(now)
	if _, exists := s.sessions[sessionID]; !exists && len(s.sessions) >= s.sessionOptions.MaxSessions {
		s.evictOldestLocked()
	}
	session := s.sessions[sessionID]
	session.History = append(session.History,
		agent.HistoryMessage{Role: "user", Content: userMessage},
		agent.HistoryMessage{Role: "assistant", Content: answer},
	)
	session.History = trimHistory(session.History, s.sessionOptions.Remote.Turns, s.sessionOptions.Remote.MaxChars)
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; media-src 'self' data:")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
