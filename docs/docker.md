# Running Bosun in Docker

An alternative to the bare-metal systemd deployment in
`docs/local-deployment.md` — useful as a clean base to add more services
(e.g. a future STT/TTS voice service) without installing anything extra on
the host.

## What's containerized (and what isn't)

Only the Bosun Go app (`smarthelper`) is containerized. The local LLM server
(llama.cpp/LM Studio) stays on the host as its own systemd service — it's
already tuned for this specific hardware (context size, thread count,
`--skip-chat-parsing`; see `docs/local-deployment.md`), and moving it into
Docker would add real risk for no benefit here.

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
docker compose build
docker compose up -d
docker compose logs -f
```

This reuses the existing `config.yaml` (mounted read-only at
`/etc/smarthelper/config.yaml`, the same third search path the bare-metal
binary already checks) and the existing `.env` file (injected as real
container environment variables via `env_file:`, not read as a dotenv file
inside the container).

Persistent data (memos, sessions, the error log) lives in the `bosun-data`
named volume, mounted at `/home/bosun/.local/share/bosun` — the same default
path the bare-metal binary uses, just inside the container. The image
pre-creates and `chown`s that directory before declaring it a volume mount
point specifically so a fresh named volume inherits the right ownership;
without that, Docker initializes new volumes as root-owned and the
non-root container user can't write to them.

## One-off commands

```bash
docker compose run --rm bosun chat "What's the weather?"
docker compose run --rm bosun errors
docker compose run --rm bosun mcp   # stdio — not useful with -it detached, see below
```

The image's `ENTRYPOINT` is `smarthelper`; `CMD` defaults to `serve`, so any
subcommand can override it directly.

## Verified

Built and run against this host's real `config.yaml` and `.env` (on a
throwaway port/volume so it didn't collide with the live systemd service):
image builds, container starts and binds correctly, reaches both the local
llama-server and the remote endpoint over the host network, `/api/status`
and `/api/chat` work, and `smarthelper chat` inside the container correctly
reports it's running under Alpine. Data volume permissions were wrong on
the first attempt (root-owned fresh volume) — fixed as described above.
