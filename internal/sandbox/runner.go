// Package sandbox implements sandboxd: the only process in this stack that
// holds /var/run/docker.sock, deliberately kept separate from the much
// larger, network-facing `bosun` service — see docs/sandbox.md for why.
// It runs one short-lived Python program per request inside a per-session
// Docker container (internal/tools.CodeExecTool is the HTTP client side,
// in the bosun process).
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// containerInfo is what the reaper needs to know about an existing
// sandbox container to reconcile its own session state on startup.
type containerInfo struct {
	Name      string
	CreatedAt time.Time
}

// containerRunner is the seam between HTTP-request handling / TTL-reaping
// logic and the real Docker CLI, so both get ordinary Go unit tests
// against a fake — see runner_test.go and server_test.go. dockerRunner
// below is the only real implementation.
type containerRunner interface {
	// EnsureRunning makes sure a long-lived workspace container named
	// `name` is running (starting a stopped one, or `docker run -d ...
	// sleep infinity` a new one from `image` if it doesn't exist yet),
	// bind-mounting workspaceDir at /workspace. No-op if already running.
	EnsureRunning(ctx context.Context, name, image, workspaceDir, memoryLimit, cpuLimit string) error
	// Exec feeds code to `python3` inside the named container's
	// /workspace and returns what it produced. The whole process tree is
	// killed (not just the direct child) if timeout elapses — see
	// execWrapperScript.
	Exec(ctx context.Context, name, code string, timeout time.Duration) (result execResult, err error)
	// Remove force-removes a container by name. Not an error if it
	// doesn't exist — the reaper and a race with a manual `docker rm`
	// shouldn't need special-casing.
	Remove(ctx context.Context, name string) error
	// ListLabeled returns every bosun-sandbox=1 container (running or
	// stopped), for the reaper's startup reconciliation.
	ListLabeled(ctx context.Context) ([]containerInfo, error)
}

// execResult is what one Exec call produced.
type execResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
}

// maxCapturedOutputBytes caps how much of a sandboxed program's stdout/
// stderr this process will buffer, independent of whatever's on the other
// side of docker exec's pipe. Without it, a script that prints a lot
// (accidentally or because the model got confused and looped) buffers
// unboundedly here and then lands whole in the model's next turn — the
// same context-overflow shape as memo.search/memo.topics' unbounded
// limits, just reached through run_code instead. 64KB is generous for
// anything a model would deliberately want back (a few thousand lines of
// text) while still bounding memory use regardless of what the script
// does.
const maxCapturedOutputBytes = 64 * 1024

// truncatingBuffer collects up to maxCapturedOutputBytes and silently
// discards anything past that, tracking whether it did. Never returns a
// write error — cmd.Run must not fail just because the program was
// chatty.
type truncatingBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *truncatingBuffer) Write(p []byte) (int, error) {
	remaining := maxCapturedOutputBytes - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

// sandboxLabel marks every container this service creates, so the reaper
// (and a human running `docker ps`) can tell them apart from anything else
// on the box — never touch a container without this label.
const sandboxLabel = "bosun-sandbox=1"

// dockerRunner shells out to the real `docker` CLI — no Docker Go SDK, to
// match this project's existing hand-rolled-over-heavy-SDK preference
// (e.g. the hand-rolled SigV4 signer in internal/backup instead of
// aws-sdk-go-v2). Every argument is a separate argv element
// (exec.CommandContext), never a shell string built by concatenation — the
// same subprocess discipline internal/webui/pdf.go and
// internal/alerts/speaker.go already use. `code` (arbitrary, LLM-authored
// text) only ever travels over stdin, never as an argv element or inside
// a shell-interpreted string.
type dockerRunner struct{}

func (dockerRunner) EnsureRunning(ctx context.Context, name, image, workspaceDir, memoryLimit, cpuLimit string) error {
	state, err := dockerCombinedOutput(ctx, "inspect", "--format", "{{.State.Running}}", name)
	switch {
	case err == nil && strings.TrimSpace(state) == "true":
		return nil // already running
	case err == nil: // exists but stopped
		if _, err := dockerCombinedOutput(ctx, "start", name); err != nil {
			return fmt.Errorf("start existing sandbox container %s: %w", name, err)
		}
		return nil
	}
	// `docker inspect` failed — assume "no such object" and create fresh.
	// --network host: full LAN/internet access, explicitly requested; see
	// docs/sandbox.md's "must never" list for the flags this deliberately
	// excludes (--privileged, --cap-add, --pid=host, --ipc=host, any
	// unrestricted host bind mount).
	args := []string{
		"run", "-d",
		"--name", name,
		"--label", sandboxLabel,
		"--network", "host",
		"--memory", memoryLimit,
		"--cpus", cpuLimit,
		"-v", workspaceDir + ":/workspace",
		"-w", "/workspace",
		image,
		"sleep", "infinity",
	}
	if _, err := dockerCombinedOutput(ctx, args...); err != nil {
		return fmt.Errorf("create sandbox container %s: %w", name, err)
	}
	return nil
}

// execWrapperScript runs code (fed over stdin, captured to a temp file
// first — a backgrounded process's stdin is not reliably delivered on
// this image's shell, confirmed empirically) as its own process-group
// leader, and kills the *entire group* — not just the direct child — if
// it's still running after timeoutSeconds. Verified live: a script that
// spawns its own subprocess ("import subprocess; ...") still gets fully
// reaped, not just the immediate python3 process. Exit code 137
// (SIGKILL) after the requested timeout means TimedOut; busybox `timeout`
// alone was tried first and confirmed NOT sufficient (it only signals the
// direct child, leaving a backgrounded grandchild running past the
// deadline) — see docs/sandbox.md.
const execWrapperScript = `CODE_FILE=$(mktemp)
cat > "$CODE_FILE"
PGID=$$
python3 "$CODE_FILE" &
CHILD=$!
( sleep %d; kill -KILL -- -$PGID 2>/dev/null ) &
WATCHER=$!
wait $CHILD
CODE=$?
kill $WATCHER 2>/dev/null
rm -f "$CODE_FILE"
exit $CODE
`

func (dockerRunner) Exec(ctx context.Context, name, code string, timeout time.Duration) (execResult, error) {
	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	script := fmt.Sprintf(execWrapperScript, seconds)

	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", name, "sh", "-c", script)
	cmd.Stdin = strings.NewReader(code)
	var stdout, stderr truncatingBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			return execResult{}, fmt.Errorf("run code in sandbox container %s: %w", name, runErr)
		}
		exitCode = exitErr.ExitCode()
	}
	// 137 = 128 + SIGKILL(9), exactly what execWrapperScript's group-kill
	// produces on a real timeout. Also what an OOM-kill would produce —
	// an accepted, documented imprecision (docs/sandbox.md): either way,
	// the process didn't finish on its own, which is what actually
	// matters to report back to the model.
	return execResult{
		Stdout:          stdout.buf.String(),
		Stderr:          stderr.buf.String(),
		ExitCode:        exitCode,
		TimedOut:        exitCode == 137,
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
	}, nil
}

func (dockerRunner) Remove(ctx context.Context, name string) error {
	_, err := dockerCombinedOutput(ctx, "rm", "-f", name)
	// "No such container" isn't a real failure — the point of Remove is
	// "make sure it's gone," and it already is.
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

func (dockerRunner) ListLabeled(ctx context.Context) ([]containerInfo, error) {
	out, err := dockerCombinedOutput(ctx, "ps", "-a",
		"--filter", "label="+sandboxLabel,
		"--format", "{{.Names}}\t{{.CreatedAt}}")
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}
	var containers []containerInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		info := containerInfo{Name: fields[0]}
		if len(fields) == 2 {
			// Docker's own `{{.CreatedAt}}` format, e.g. "2026-08-22
			// 10:00:00 +0000 UTC" — best-effort; an unparseable value just
			// means the reaper treats it as "created now" instead of
			// erroring the whole reconciliation over one row.
			if parsed, err := time.Parse("2006-01-02 15:04:05 -0700 MST", fields[1]); err == nil {
				info.CreatedAt = parsed
			}
		}
		containers = append(containers, info)
	}
	return containers, nil
}

func dockerCombinedOutput(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}
