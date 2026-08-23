package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// fakeRunner stands in for the real Docker CLI (dockerRunner) in tests —
// no Docker daemon needed for `go test ./...`; the real dockerRunner path
// is verified manually against the live host instead (see docs/sandbox.md).
type fakeRunner struct {
	mu         sync.Mutex
	running    map[string]bool
	created    map[string]containerInfo
	execCalls  []fakeExecCall
	execResult execResult
	execErr    error
	ensureErr  error
	removeErr  error
}

type fakeExecCall struct {
	Name    string
	Code    string
	Timeout time.Duration
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{running: map[string]bool{}, created: map[string]containerInfo{}}
}

func (f *fakeRunner) EnsureRunning(ctx context.Context, name, image, workspaceDir, memoryLimit, cpuLimit string) error {
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[name] = true
	if _, exists := f.created[name]; !exists {
		f.created[name] = containerInfo{Name: name, CreatedAt: time.Now()}
	}
	return nil
}

func (f *fakeRunner) Exec(ctx context.Context, name, code string, timeout time.Duration) (execResult, error) {
	f.mu.Lock()
	f.execCalls = append(f.execCalls, fakeExecCall{Name: name, Code: code, Timeout: timeout})
	f.mu.Unlock()
	if f.execErr != nil {
		return execResult{}, f.execErr
	}
	if !f.running[name] {
		return execResult{}, fmt.Errorf("exec on non-running container %s", name)
	}
	return f.execResult, nil
}

func (f *fakeRunner) Remove(ctx context.Context, name string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, name)
	delete(f.created, name)
	return nil
}

func (f *fakeRunner) ListLabeled(ctx context.Context) ([]containerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	containers := make([]containerInfo, 0, len(f.created))
	for _, info := range f.created {
		containers = append(containers, info)
	}
	return containers, nil
}
