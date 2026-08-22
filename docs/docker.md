# Running Bosun in Docker

An alternative to the bare-metal systemd deployment in
`docs/local-deployment.md` — useful as a clean base to add more services
(e.g. a future STT/TTS voice service) without installing anything extra on
the host.

## What's containerized

Three services, all in `docker-compose.yml`: the Bosun Go app (`bosun`), and
two `llama-server` instances (`llama-chat` for the chat model, `llama-embed`
for the semantic-search embedding model — see `docs/memo-search.md`).

`llama-chat`/`llama-embed` are built from `deploy/llama/Dockerfile`, which
compiles llama.cpp from source at a pinned commit instead of pulling the
official `ghcr.io/ggml-org/llama.cpp` image. This host's CPU (an old Mac
Mini) needs the portable, runtime-dispatching CPU backend
(`GGML_CPU_ALL_VARIANTS`, `GGML_NATIVE=OFF`) to avoid an illegal-instruction
crash on newer, assumed-available instruction sets — see the Dockerfile's
own comments and `~/rebuild-llama.cpp.sh` on this host for the exact flags
this mirrors. Both services run the same image with different `command:`
overrides. Model files are downloaded via `-hf` into the host's
`~/.cache/huggingface` (bind-mounted via `${LLAMA_HF_CACHE}` in `.env`, not
committed) so they aren't re-fetched on every container recreate.

## Networking

The compose file uses `network_mode: host`. Bosun is a LAN-only, no-auth
personal appliance that binds to an explicit private IP
(`web.bind` in `config.yaml`) and needs to reach the local LLM server at
`localhost:1234` — host networking makes both of those work exactly like the
bare-metal deployment, with no port-mapping or `host-gateway` alias needed.
This is a deliberate choice for a single-host LAN box, not a default to
reuse blindly in a multi-host or internet-facing setup.

## Build and run

```bash
make docker-up      # = docker compose build && docker compose up -d
docker compose logs -f
```

`docker-compose.yml` pins the Compose project name explicitly
(`name: bosun-smarthelper`) rather than leaving it to Compose's own
default — the checkout directory's basename. Without that, renaming or
relocating the checkout would make a future `docker compose` invocation
compute a different project name than the one already-running containers
were created under, so it would fail to recognize them as its own stack
at all — pinning it keeps `docker compose` working the same regardless of
what the directory is called or where it lives.

`.env` must set `LLAMA_HF_CACHE` (host path to the Hugging Face cache used
by `-hf` downloads, e.g. `~/.cache/huggingface`) — see `.env.example`. The
first `llama-chat`/`llama-embed` build compiles llama.cpp from source and
takes a while; later builds hit Docker's layer cache and are fast unless
`deploy/llama/Dockerfile` or the pinned commit changes.

This reuses the existing `config.yaml` (mounted read-only at
`/etc/smarthelper/config.yaml`, the same third search path the bare-metal
binary already checks) and the existing `.env` file (injected as real
container environment variables via `env_file:`, not read as a dotenv file
inside the container).

Persistent data (memos, documents, sessions, the error log) lives in
`./data/bosun` on the host, bind-mounted at
`/home/bosun/.local/share/bosun` — the same default path the bare-metal
binary uses, just inside the container. This is a **plain host
directory**, not a Docker-managed named volume: `docker compose down -v`,
removing images, or uninstalling Docker entirely can't touch it. That
only works because the container's `bosun` user is `uid`/`gid` 1000
(matching this host's own user) — the image pre-creates and `chown`s the
directory before declaring the mount point so it's correctly owned from
the first container start, and a host-owned mismatch would otherwise
leave the container unable to write to it.

## One-off commands

```bash
docker compose run --rm bosun chat "What's the weather?"
docker compose run --rm bosun errors
docker compose run --rm bosun mcp   # stdio — not useful with -it detached, see below
```

The image's `ENTRYPOINT` is `smarthelper`; `CMD` defaults to `serve`, so any
subcommand can override it directly.

## Verified

Built and run against this host's real `config.yaml` and `.env`: `bosun`
reaches both `llama-chat` and the remote endpoint over the host network,
`/api/status` and a streaming `/api/chat` turn (including a tool call) work
end to end against the dockerized local model, and `llama-embed` returns
correct multilingual embeddings. The former bare-metal `llama-server.service`
systemd unit is stopped and disabled — `llama-chat`/`llama-embed` are now
the only thing serving ports 1234/1235.
