# Documentation index

Everything here goes deeper on one feature or decision than `README.md`'s
top-level tour has room for. Grouped by what you're trying to do.

- **[why-bosun.md](why-bosun.md)** — the problem this project actually
  solves (a boat/RV with no reliable internet) and the one thing neither a
  cloud assistant nor fixed marine electronics can do on their own. Start
  here if you're wondering what this is *for*, not just what it does.

## Deploying and running it

- **[../INSTALL.md](../INSTALL.md)** — complete, tested, from-zero install
  walkthrough (fresh Ubuntu → running web UI), Docker-based. Start here if
  you're setting this up for the first time.
- **[backup.md](backup.md)** — `smarthelper backup`/`restore`: archives
  persistent data (memos, documents, sessions, metrics — dumped to SQL,
  not copied raw) to any S3-compatible bucket, and restores it into a
  fresh directory. Manual by default (a settings-page toggle can turn on
  an interval-based schedule instead — off unless you opt in, so nothing
  spends bandwidth uninvited by default); a hand-rolled SigV4 signer
  (Put/Get/List) instead of the AWS SDK.
- **[docker.md](docker.md)** — the actual way this project runs today:
  `bosun` + `llama-chat`/`llama-embed` (built from source) + `whisper-stt`
  under `docker compose`, networking, volumes, one-off commands.
- **[local-deployment.md](local-deployment.md)** — historical, bare-metal
  systemd setup, retired in favor of Docker. Kept for the still-relevant
  reasoning (`--skip-chat-parsing`, CPU-backend tuning) that
  `deploy/llama/Dockerfile` implements instead now.

## Reaching it from outside the LAN

- **[tls.md](tls.md)** — HTTPS for a LAN IP that has no public domain
  (mkcert as a private CA). Superseded on this specific deployment by a
  real domain + Cloudflare (below), but still the right approach for a
  deployment with no domain to use.
- **[cloudflare.md](cloudflare.md)** — a real domain, a real Let's
  Encrypt certificate via DNS-01, an outbound-only `cloudflared` tunnel
  for remote access, split-horizon DNS so the same URL resolves directly
  on the LAN, and a Cloudflare Access gate in front of the tunnel.

## Core features

- **[memo-search.md](memo-search.md)** — persistent memos and uploaded
  reference documents (manuals, how-tos), searched by meaning via local
  embeddings. Covers chunking, image/diagram handling and OCR, the
  relevance floor and repetition-guard that keep a weak model from going
  off the rails, and `documents.Store.AttachOrphanedImages` for merging a
  diagram onto the text chunk that actually covers it.
- **[maintenance-tracking.md](maintenance-tracking.md)** — logging
  counter/date-based equipment upkeep (oil changes, engine-hour meters,
  anything with a due date) as optional fields on a regular memo, and the
  `maintenance` action that reports what's due — domain-neutral by design,
  so a car's odometer and a boat's main-engine hours both fit without a
  schema change. Also covers the metric-merge approval queue: a periodic
  LLM check proposes merging counters that look like the same equipment
  under two names, but a human always approves or rejects before anything
  actually renames.
- **[voice.md](voice.md)** — push-to-talk speech input (whisper.cpp) and
  spoken replies (Piper TTS), fully offline, no Python in the path; the
  Alpine/musl-vs-glibc onnxruntime saga that shaped how it's built.
- **[monitoring.md](monitoring.md)** — the 📊 dashboard: a local,
  bounded-history time-series store (SQLite) for CPU/memory/disk/GPS/
  fridge, sampled on an interval. What to sample is a `config.yaml` list,
  not hardcoded, so a future sensor is a config edit, not a Go change.
- **[settings.md](settings.md)** — the gear-icon dialog for editing
  assistant name/style, default language, LLM temperatures, and the memo
  tag vocabulary live, without touching `config.yaml`.
- **[alerts.md](alerts.md)** — NOAA weather alerts for the current
  position and any `config.yaml`-defined metric threshold (disk space,
  and — once the sensor exists — a battery charge or tank level, no code
  change needed), delivered via Telegram, a webhook, or spoken through the
  host's own speaker; each channel is on only once it's both configured
  and enabled from the settings page.
- **[sandbox.md](sandbox.md)** — `run_code`: the LLM writes and runs a
  short Python program for math/parsing/simulation it's otherwise bad at.
  Never executes inside `bosun` itself — a separate `sandboxd` service (the
  only thing holding `/var/run/docker.sock`) runs it in its own Docker
  container instead. Off by default, and needs two separate opt-ins to run
  at all.
- **[cameras.md](cameras.md)** — a 📹 button for one or more WiFi
  cameras' live view and recorded archive. A relay (`internal/cameras`)
  holds each camera's one client slot and fans frames out to any number
  of viewers plus the archive recorder, since these cameras typically
  accept only a single connection.
- **[adventure.md](adventure.md)** — a voice-playable text adventure
  (Colossal Cave Adventure), for when there's nothing to do, no
  connection, or you're driving. The engine is a separate, zero-dependency
  public repo ([go-adventure](https://github.com/rromenskyi/go-adventure));
  `internal/adventure` here is SQLite session persistence plus an LLM
  tool. Off by default.
- **[filedump.md](filedump.md)** — a 📄 button for a general-purpose,
  drag-and-drop-organizable file tree (any file type, stored as-is,
  real folders), with a per-upload checkbox to also feed a file into
  `memo-search.md`'s RAG document store, tagged with the folder it came
  from. Off by default.

## How it behaves, and why

- **[streaming.md](streaming.md)** — why the web UI's answers fill in
  progressively, how a tool call's raw encoding gets folded into a
  collapsed detail instead of ever appearing as raw JSON, and why the CLI
  and MCP paths are unaffected.
- **[offline-mode.md](offline-mode.md)** — how connectivity state decides
  both the LLM provider and which tools are even offered to it, and the
  contract a new online-only tool needs to follow.
- **[token-budget.md](token-budget.md)** — why the local fallback model
  gets a compact tool-definition format and a tight history budget while
  the remote model doesn't need either, and where those knobs live.

## Elsewhere

- **`../README.md`** — project overview, quick start, MCP tool reference,
  configuration reference.
- **`../SPEC.md`** — the full roadmap: what's shipped, what's next.
- **`../AGENTS.md`** — code style and testing expectations for anyone
  (human or agent) changing this codebase.
