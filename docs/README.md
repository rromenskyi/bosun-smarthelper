# Documentation index

Everything here goes deeper on one feature or decision than `README.md`'s
top-level tour has room for. Grouped by what you're trying to do.

## Deploying and running it

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
