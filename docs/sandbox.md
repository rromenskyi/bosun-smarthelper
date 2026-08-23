# `run_code`: a Python execution sandbox

Bosun's local model (0.8B–2B) is genuinely bad at real computation — math,
parsing, simulation. `run_code` lets the LLM write a short Python 3
program and get its stdout/stderr/exit code back, instead of reasoning
through the arithmetic itself. Off by default; see "Enabling it" below.

## Why a separate `sandboxd` service, not bosun itself

The code this tool runs is entirely LLM-authored — arbitrary text, not
reviewed by a human before it runs. The one non-negotiable requirement is
that it can never compromise the actual host machine, only its own
sandbox.

A pure-Go namespace/chroot sandbox running *inside* the existing `bosun`
container was tried first and ruled out — verified live, not assumed:

```
$ docker exec bosun sh -c 'unshare --user --map-root-user id'
unshare: unshare(0x10000000): Operation not permitted
```

Docker's default seccomp profile blocks `clone(CLONE_NEWUSER)` for a
non-privileged container even though the host kernel allows unprivileged
user namespaces. Loosening `bosun`'s own seccomp profile to work around
that would make the largest, most network-exposed service in this whole
stack (a public Cloudflare tunnel sits in front of it) more dangerous just
to sandbox something else — not an acceptable trade.

So this uses **Docker itself** as the isolation boundary, via a
**separate, minimal control-plane service** (`sandboxd`) rather than
giving `bosun` the Docker socket directly. `/var/run/docker.sock` is
root-equivalent on the host (trivially: `docker run -v /:/host
--privileged ...`) — whoever holds it already *is* root. `bosun` is the
single most exposed process in this stack; giving it that socket directly
would mean any future bug in it (a path traversal, a dependency CVE, a
parsing bug) is one step from host root. Isolating socket access into one
small, single-endpoint, loopback-only service shrinks the trusted
computing base to exactly the code that touches it.

## Architecture

```
LLM calls run_code(code)
        │
        ▼
internal/tools.CodeExecTool.Execute        (in the bosun container)
        │  POST http://127.0.0.1:8090/run
        │  {session_id, code, timeout_seconds}
        ▼
internal/sandbox.Server.handleRun          (in the sandboxd container —
        │  re-validates session_id, clamps      the ONLY thing with
        │  timeout, ensures/reuses a workspace   /var/run/docker.sock)
        │  docker exec -i <name> sh -c <wrapper script>
        ▼
bosun-sandbox-<session-id> container       (--network host, --memory,
   FROM bosun-sandbox-python:local            --cpus; never --privileged,
   (built locally, pinned digest)              --cap-add, --pid=host,
                                                --ipc=host, or an
                                                unrestricted host mount)
```

- **`session_id` is never LLM-supplied.** It's the real webui chat
  session ID (already `crypto/rand`-generated and validated by
  `internal/webui/server.go`'s `newSessionID`/`validSessionID`),
  threaded through `context.Context` into `CodeExecTool.Execute` the same
  way `ctx` already reaches every tool call
  (`internal/agent/agent.go`'s `executeTool`). The CLI (`smarthelper
  chat`) and MCP paths have no session concept at all, so
  `CodeExecTool` falls back to one shared constant
  (`tools.DefaultCodeExecSessionID`) there. This keeps `session_id`
  entirely out of the tool's LLM-facing schema — the model can't forge or
  collide a workspace, and the weak local model was also going to be bad
  at reusing an ID consistently across calls anyway.
- **`sandboxd` re-validates `session_id`** against the same strict
  `[a-zA-Z0-9_-]{8,128}` rule regardless of what `bosun` sends — it
  becomes both a container name and a filesystem path component, so this
  can't be "trust the caller."
- **Code travels over stdin**, never a shell string or argv element —
  `internal/sandbox/runner.go` uses `exec.CommandContext` with argv
  slices throughout, the same subprocess discipline
  `internal/webui/pdf.go` and `internal/alerts/speaker.go` already use.

## What this deliberately does *not* protect against

Decided explicitly, not an oversight:

- **The executed code has full network access — LAN and public
  internet.** Sandbox containers run with `--network host`, same as
  every other service in this stack. This means it can reach `bosun`'s
  own loopback-only sibling services (`llama-chat`, `whisper-stt` — both
  no-auth by design already) and anything else on the LAN. If that's ever
  a concern, `--network host` on the sandbox container specifically
  (`internal/sandbox/runner.go`'s `EnsureRunning`) is the one flag to
  change — it's orthogonal to the actual host-isolation mechanism (mount/
  PID namespaces + cgroups), so narrowing it later doesn't touch anything
  else in this design.
- **Resource limits (`memory_limit`/`cpu_limit`) exist for reliability,
  not security.** This box also runs LLM inference and the web server; a
  runaway script from the weak local model shouldn't be able to hang or
  OOM the one physical machine everything else depends on. They are not
  meant to stop a determined adversary.
- **A workspace container survives for `session_ttl`, not just one
  call.** This is a real, if modest, trade: the *only* thing enforcing
  the non-negotiable (can't touch the real host) is Docker's own
  container boundary, and a long-lived, network-reachable container
  extends the window during which a genuine container-escape bug (runc/
  kernel CVEs happen periodically) could be triggered from outside,
  compared to a `--rm`-per-call container that exists for seconds. Kept
  anyway because it's what makes a multi-step task's files persist across
  calls — just keep the host kernel and Docker reasonably patched, since
  that's the entire remaining defense.

## The "must never" list

For anyone editing `internal/sandbox/runner.go`'s `docker run`/`docker
exec` invocations later: never add `--privileged`, any `--cap-add`,
`--pid=host`, `--ipc=host`, or an unrestricted `-v /:/...`-style host
mount. Any one of these silently defeats the entire isolation story this
document just spent several paragraphs justifying. `--network host` is
the one already-deliberate exception (see above) — it only shares the
network namespace and has no effect on the filesystem/PID isolation that
actually matters.

## Killing the whole process tree, not just the direct child

Busybox `timeout` (used by `python:3.12-alpine`, the runtime image) was
tried first and confirmed, empirically, **not sufficient**: it only
signals the direct child. A script that backgrounds its own subprocess
(`subprocess.Popen(...)`, or even a plain `foo &` inside the executed
code) keeps running past the nominal deadline, orphaned inside the shared,
reused container, until the whole container is eventually TTL-reaped.

`internal/sandbox/runner.go`'s `execWrapperScript` instead runs the code
as its own session/process-group leader (`setsid`-equivalent via a
`PGID=$$` capture) and kills the **entire group** (`kill -KILL -- -$PGID`)
if a background watcher fires first. Verified live: a script that spawns
a subprocess that would otherwise `touch` a marker file after the nominal
timeout does *not* get to — the marker never appears. Exit code 137
(SIGKILL) is treated as `timed_out: true`; this is also what an OOM-kill
produces, an accepted imprecision (either way, the process didn't finish
on its own, which is what matters for the response).

Stdin is captured to a temp file (`cat > "$CODE_FILE"`, in the foreground)
*before* backgrounding the actual Python process — also confirmed
empirically necessary: piping stdin directly into a backgrounded command
on this image's shell does not reliably deliver it.

**Known gap, accepted, not a security hole**: this kill is by process
group, not by cgroup. Code that explicitly detaches into its *own* new
session — Python's `subprocess.Popen(..., start_new_session=True)`, or an
equivalent `setsid()` call — gets a new PGID and is no longer reachable by
`kill -KILL -- -$PGID`, so it keeps running past the per-call
`timeout_seconds`. This doesn't threaten the one non-negotiable (it's
still fully contained inside the same disposable container, still bounded
by that container's `--memory`/`--cpus` cgroup limits regardless of which
process group it's in, and still gets cleaned up whenever the session's
TTL eventually reaps the whole container) — it just means the wall-clock
timeout specifically isn't airtight against deliberately evasive code,
consistent with that timeout always having been a reliability guard, not
a security control (see "What this deliberately does not protect
against" above).

## Enabling it

**Two separate switches, deliberately** — `sandbox.enabled` in
`config.yaml` only controls whether `bosun` registers the tool; it has no
power over whether `sandboxd` (and its root-equivalent socket access) is
running at all. Both are required:

1. `config.yaml`:
   ```yaml
   sandbox:
     enabled: true
   ```
2. `.env`:
   ```
   COMPOSE_PROFILES=sandbox
   ```
   `sandboxd` carries a Compose profile (`profiles: ["sandbox"]`
   in `docker-compose.yml`) specifically so a plain `docker compose up -d`
   never starts a root-equivalent socket holder by accident.

Then `docker compose up -d` (picks up `COMPOSE_PROFILES` from `.env`
automatically) and restart `bosun` to pick up the config change.

`make docker-build`/`docker-build` already builds both `sandboxd`'s own
image and the locally built `bosun-sandbox-python:local` runtime image
regardless of whether the feature is enabled — cheap, and it means turning
this on later is a config edit, not another build.

## Session/TTL behavior

- Each chat session gets its own workspace container
  (`bosun-sandbox-<session-id>`), created on first use and reused
  (`docker exec`, not a fresh `docker run`) on every subsequent call —
  files written to `/workspace` persist across calls within the same
  conversation.
- Idle past `sandbox.session_ttl` (default 15 minutes), the container and
  its scratch directory are removed by a reaper goroutine
  (`internal/sandbox/reaper.go`), ticking every 2 minutes — the same
  ticker-goroutine shape `runBackupScheduler`/`runTagNormalizer`
  (`cmd/smarthelper/main.go`) already use.
- Last-used timestamps are persisted (`sandbox_sessions.json` in
  `sandbox.state_dir`, the same `atomicWriteJSON`-via-temp-file-plus-
  rename pattern as `internal/backup/schedule.go`/`internal/alerts/
  state.go`) — a `sandboxd` restart reconciles against whatever
  containers Docker actually reports (`internal/sandbox.Reconcile`)
  rather than either immediately reaping a still-live session or leaking
  one forever.

## Image pinning

`deploy/sandbox-runtime/Dockerfile` pins `python:3.12-alpine` by digest,
not a floating tag — matching `deploy/llama/Dockerfile`'s pinned commit
and `deploy/piper`'s pinned `PIPER_REF`. A `docker compose build` months
from now must not silently change what's inside the isolation boundary;
re-pin deliberately (a reviewed comment update) if the base image ever
needs bumping.
