// Package webui serves the LAN-only browser interface for Smart Helper.
package webui

import (
	"bytes"
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
	"github.com/roman220/ai-local-smarthelper/internal/documents"
	"github.com/roman220/ai-local-smarthelper/internal/settings"
)

const maxRequestBody = 16 * 1024

// maxDocumentUploadBytes bounds a reference-document upload (e.g. a car
// manual as plain text) — generous compared to maxRequestBody's normal chat
// message cap, but still bounded.
const maxDocumentUploadBytes = 2 << 20

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
	mux.HandleFunc("GET /api/documents", s.handleDocumentsList)
	mux.HandleFunc("POST /api/documents", s.handleDocumentUpload)
	mux.HandleFunc("POST /api/documents/pages", s.handleDocumentAddPages)
	mux.HandleFunc("DELETE /api/documents/{id}", s.handleDocumentDelete)
	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("POST /api/settings", s.handleSettingsUpdate)
	mux.HandleFunc("GET /ca.pem", s.handleCACert)
	if s.documentImagesDir != "" {
		mux.Handle("GET /document-images/", http.StripPrefix("/document-images/", http.FileServer(http.Dir(s.documentImagesDir))))
	}
	return securityHeaders(mux)
}

// Serve listens until ctx is cancelled. When both certFile and keyFile are
// non-empty it serves HTTPS (e.g. certs from mkcert — see docs/tls.md);
// otherwise plain HTTP, matching every deployment before TLS support existed.
func (s *Server) Serve(ctx context.Context, address, certFile, keyFile string) error {
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
		if certFile != "" && keyFile != "" {
			errCh <- server.ListenAndServeTLS(certFile, keyFile)
		} else {
			errCh <- server.ListenAndServe()
		}
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
	w.Header().Set("Cache-Control", "no-store")
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

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()

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
	s.saveTurn(sessionID, request.Message, answer)
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
	return func(event streamEvent) {
		_ = encoder.Encode(event)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

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

	answer, err := asker.AskWithHistoryStreaming(ctx, message, history, language, func(e agent.StepEvent) {
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

	s.saveTurn(sessionID, message, answer)
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

// handleDocumentUpload accepts a plain-text or PDF file via a multipart
// form with "title" and "file" fields. A PDF is split page by page
// (poppler-utils): pages with an extractable text layer become text
// chunks, pages without one (diagrams, scans) become a rendered image —
// see pdf.go and docs/memo-search.md for what that does and doesn't cover
// (no OCR). This is a human-only path: the agent's tool contract never
// exposes document ingestion, only search, to keep the tool schema small
// for weak local models.
func (s *Server) handleDocumentUpload(w http.ResponseWriter, r *http.Request) {
	if s.documents == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "document search is not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentUploadBytes)
	if err := r.ParseMultipartForm(maxDocumentUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upload"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file is required"})
		return
	}
	defer file.Close()
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = header.Filename
	}
	content, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read file"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()

	if bytes.HasPrefix(content, pdfMagic) {
		pages, err := extractPDFPages(ctx, content, s.documentImagesDir, "/document-images/")
		if err != nil {
			s.logger.Error("extract pdf", "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not process PDF"})
			return
		}
		summary, err := s.documents.AddPages(ctx, title, pages)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, summary)
		return
	}
	// Only plain text and PDF are supported — any other binary file would
	// otherwise silently "upload" as garbage, since there's no
	// format-specific parsing for it.
	if !utf8.Valid(content) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file must be plain UTF-8 text or a PDF"})
		return
	}

	summary, err := s.documents.Add(ctx, title, string(content))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

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
// itself. Ordinary uploads go through handleDocumentUpload instead.
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
	summary, err := s.documents.AddPages(ctx, request.Title, pages)
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
