package sandbox

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// validSessionID mirrors internal/webui/server.go's own validSessionID
// exactly (crypto/rand hex, 8-128 chars, [a-zA-Z0-9_-]) — duplicated
// rather than shared across these two separate binaries/packages, the
// same "small state/validation idioms are copied, not abstracted into a
// shared util package" convention this project already uses elsewhere
// (e.g. atomicWriteJSON exists separately in internal/backup and
// internal/alerts). session_id is re-validated here regardless of what
// bosun's CodeExecTool already guarantees on its side — it becomes both a
// container name and a filesystem path component, so this can't be
// "trust the caller."
var validSessionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`).MatchString

// Server is sandboxd's HTTP handler — the only thing in this stack that
// causes a `docker run`/`docker exec` against a real Docker daemon (via
// Runner). See docs/sandbox.md for why this is a separate service from
// `bosun` rather than bosun holding /var/run/docker.sock directly.
type Server struct {
	Runner       containerRunner
	Tracker      *sessionTracker
	ScratchRoot  string
	RuntimeImage string
	MemoryLimit  string
	CPULimit     string
	// DefaultTimeout is used when a request doesn't specify one;
	// MaxTimeout is a hard ceiling regardless of what's requested — both
	// reliability limits (this box also runs LLM inference), not security
	// ones.
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	Logger         *slog.Logger
}

// NewServer wires a Server with the real Docker CLI runner.
func NewServer(tracker *sessionTracker, scratchRoot, runtimeImage, memoryLimit, cpuLimit string, defaultTimeout, maxTimeout time.Duration, logger *slog.Logger) *Server {
	return &Server{
		Runner:         dockerRunner{},
		Tracker:        tracker,
		ScratchRoot:    scratchRoot,
		RuntimeImage:   runtimeImage,
		MemoryLimit:    memoryLimit,
		CPULimit:       cpuLimit,
		DefaultTimeout: defaultTimeout,
		MaxTimeout:     maxTimeout,
		Logger:         logger,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/run", s.handleRun)
	return mux
}

// runRequest/runResponse mirror internal/tools.codeExecRequest/
// codeExecResponse exactly — the one HTTP contract between the two
// separate binaries, kept in sync by hand (see the comment there).
type runRequest struct {
	SessionID      string `json:"session_id"`
	Code           string `json:"code"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type runResponse struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

// maxRunRequestBody bounds the JSON body handleRun will read — generous
// for a real Python program (about 1500 lines of ASCII), but not
// unbounded. Matters more here than for a typical small API request:
// sandbox containers run with --network host (see runner.go), so code
// executing inside the very sandbox this service isolates can reach
// sandboxd's own loopback listener and would otherwise be able to POST
// an arbitrarily large body at it.
const maxRunRequestBody = 64 * 1024

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRunRequestBody)
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validSessionID(req.SessionID) {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	timeout := s.DefaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	if timeout > s.MaxTimeout {
		timeout = s.MaxTimeout
	}
	if timeout < time.Second {
		timeout = time.Second
	}

	containerName := "bosun-sandbox-" + req.SessionID
	workspaceDir := filepath.Join(s.ScratchRoot, req.SessionID)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create workspace: %v", err))
		return
	}

	// Touched before EnsureRunning/Exec, not after — the reaper's 2-minute
	// tick has no way to know a session is mid-request otherwise. A
	// session sitting just under SessionTTL that then starts a call
	// taking up to MaxTimeout (120s) would otherwise still read as idle
	// for the whole call, and sweep() could remove its container out from
	// under an in-flight docker exec.
	if err := s.Tracker.Touch(req.SessionID); err != nil && s.Logger != nil {
		s.Logger.Warn("persist sandbox session state", "session", req.SessionID, "error", err)
	}

	ctx := r.Context()
	if err := s.Runner.EnsureRunning(ctx, containerName, s.RuntimeImage, workspaceDir, s.MemoryLimit, s.CPULimit); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("start sandbox: %v", err))
		return
	}

	result, err := s.Runner.Exec(ctx, containerName, req.Code, timeout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("run code: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, runResponse{
		Stdout:          result.Stdout,
		Stderr:          result.Stderr,
		ExitCode:        result.ExitCode,
		TimedOut:        result.TimedOut,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, runResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
