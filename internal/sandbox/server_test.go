package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

var errStartFailed = errors.New("start failed")

func newTestServer(t *testing.T, runner *fakeRunner) *Server {
	t.Helper()
	tracker, err := NewSessionTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionTracker: %v", err)
	}
	s := NewServer(tracker, t.TempDir(), "bosun-sandbox-python:local", "512m", "1", 30*time.Second, 120*time.Second, nil)
	s.Runner = runner
	return s
}

func postRun(t *testing.T, s *Server, body map[string]any) (int, runResponse) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest("POST", "/run", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	var result runResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Code, result
}

const validSession1 = "session-aaaaaaaa"
const validSession2 = "session-bbbbbbbb"

func TestHandleRunExecutesCodeInASandboxContainer(t *testing.T) {
	runner := newFakeRunner()
	runner.execResult = execResult{Stdout: "4\n", ExitCode: 0}
	s := newTestServer(t, runner)

	status, result := postRun(t, s, map[string]any{"session_id": validSession1, "code": "print(2+2)"})
	if status != 200 {
		t.Fatalf("status = %d, body = %+v", status, result)
	}
	if result.Stdout != "4\n" || result.ExitCode != 0 {
		t.Errorf("result = %+v", result)
	}
	if len(runner.execCalls) != 1 || runner.execCalls[0].Name != "bosun-sandbox-"+validSession1 {
		t.Errorf("exec calls = %+v, want one call against bosun-sandbox-%s", runner.execCalls, validSession1)
	}
}

func TestHandleRunRejectsInvalidSessionID(t *testing.T) {
	s := newTestServer(t, newFakeRunner())
	for _, id := range []string{"", "short", "has spaces here", "../../etc/passwd"} {
		status, result := postRun(t, s, map[string]any{"session_id": id, "code": "pass"})
		if status != 400 {
			t.Errorf("session_id %q: status = %d, want 400 (result=%+v)", id, status, result)
		}
	}
}

func TestHandleRunRejectsEmptyCode(t *testing.T) {
	s := newTestServer(t, newFakeRunner())
	status, _ := postRun(t, s, map[string]any{"session_id": validSession1, "code": ""})
	if status != 400 {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestHandleRunClampsTimeoutToServerMax(t *testing.T) {
	runner := newFakeRunner()
	s := newTestServer(t, runner)
	s.MaxTimeout = 10 * time.Second

	if _, result := postRun(t, s, map[string]any{"session_id": validSession1, "code": "pass", "timeout_seconds": 999}); result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if got := runner.execCalls[0].Timeout; got != 10*time.Second {
		t.Errorf("timeout passed to runner = %v, want clamped to 10s", got)
	}
}

func TestHandleRunReusesExistingWorkspaceForSameSession(t *testing.T) {
	runner := newFakeRunner()
	s := newTestServer(t, runner)

	postRun(t, s, map[string]any{"session_id": validSession1, "code": "x = 1"})
	postRun(t, s, map[string]any{"session_id": validSession1, "code": "print(x)"})

	if len(runner.created) != 1 {
		t.Errorf("distinct containers created = %d, want 1 (same session reused)", len(runner.created))
	}
}

func TestHandleRunGivesDistinctSessionsDistinctWorkspaces(t *testing.T) {
	runner := newFakeRunner()
	s := newTestServer(t, runner)

	postRun(t, s, map[string]any{"session_id": validSession1, "code": "pass"})
	postRun(t, s, map[string]any{"session_id": validSession2, "code": "pass"})

	if len(runner.created) != 2 {
		t.Errorf("distinct containers created = %d, want 2", len(runner.created))
	}
	dir1 := filepath.Join(s.ScratchRoot, validSession1)
	dir2 := filepath.Join(s.ScratchRoot, validSession2)
	if dir1 == dir2 {
		t.Fatal("workspace dirs collided")
	}
}

func TestHandleRunSurfacesRunnerErrorAsHTTP500(t *testing.T) {
	runner := newFakeRunner()
	runner.ensureErr = errStartFailed
	s := newTestServer(t, runner)

	status, result := postRun(t, s, map[string]any{"session_id": validSession1, "code": "pass"})
	if status != 500 || result.Error == "" {
		t.Errorf("status = %d, result = %+v, want 500 with an error message", status, result)
	}
}

func TestHandleRunOnlyAcceptsPost(t *testing.T) {
	s := newTestServer(t, newFakeRunner())
	request := httptest.NewRequest("GET", "/run", nil)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != 405 {
		t.Errorf("status = %d, want 405", response.Code)
	}
}
