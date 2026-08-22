# Installing Bosun from scratch

A complete, from-zero walkthrough for a fresh Ubuntu machine — no prior
Docker, Go, or Ollama experience assumed. Every command below was actually
run, in order, against a clean **Ubuntu 26.04 LTS** install; nothing here
is written from memory.

By the end you'll have a fully working, fully offline local assistant —
web chat, its own local LLM, memo/document search — running from `docker
compose`, reachable at `http://localhost:8080` on the machine itself.
Wiring up a phone-reachable LAN address, HTTPS, voice, or real sensor
hardware are separate, optional steps linked at the bottom; this guide's
only job is getting you to a running container stack with zero surprises.

## What you'll end up with

```
docker compose up
  ├── bosun         the web UI + chat API + all the tools (this repo)
  ├── llama-chat    a local LLM (llama.cpp, built from source, ~2B params)
  ├── llama-embed   a small local embeddings model, for memo/document search
  └── whisper-stt   speech-to-text for the 🎤 button (optional to use)
```

Nothing here calls out to a paid API by default — `llama-chat`/`llama-embed`
run entirely on your own machine. A remote provider (OpenAI or compatible)
is optional and can be added later; skipping it just means you're always
on the local model.

## 1. Install Docker

Ubuntu's own `docker.io` package works, but Docker's official repo stays
current with Compose v2 (this project's `docker-compose.yml` needs it) —
this is the same sequence from
[docs.docker.com](https://docs.docker.com/engine/install/ubuntu/), verified
against a clean 26.04 container:

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl git

sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update

sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Add yourself to the `docker` group so you don't need `sudo` for every
`docker` command (log out and back in — or just run `newgrp docker` — for
this to take effect):

```bash
sudo usermod -aG docker "$USER"
newgrp docker
```

Confirm it worked:

```bash
docker --version
docker compose version
```

## 2. Get the code

```bash
git clone https://github.com/rromenskyi/bosun-smarthelper.git
cd bosun-smarthelper
```

## 3. Configure

Two files, both git-ignored (never committed, never shared) — copy the
tracked examples:

```bash
cp .env.example .env
cp configs/config.yaml.example config.yaml
```

**That's it for a local-only test drive.** `config.yaml.example`'s
`llm.local` section already matches the `llama-chat` service above
exactly (same port, same model alias) — nothing to edit there. `.env`'s
`LOCAL_LLM_API_KEY` is a shared secret between `bosun` and its own
`llama-chat` container, not an external credential; the placeholder value
works as-is since both sides read the exact same file.

Two things worth doing anyway before you build:

- **`.env`'s `LLAMA_HF_CACHE`** — set it to a real path, e.g.
  `LLAMA_HF_CACHE=/home/$USER/.cache/huggingface`. This is where the local
  models get downloaded to on first run; without a real path here they'd
  just get re-downloaded (a few GB) every time the containers are
  recreated.
- **`OPENAI_API_KEY`** — optional. Leave the placeholder if you don't have
  one: a request to the (non-functional) remote provider fails instantly
  and the router silently falls back to the local model, every time — see
  `internal/llm/router.go`'s `Chat`. Fill in a real key later, any time,
  no rebuild needed, to add a remote model into the mix (`docs/docker.md`,
  `README.md`'s Configuration Reference).

## 4. Build and run

```bash
make docker-up      # = docker compose build && docker compose up -d
docker compose logs -f
```

The first build compiles `llama.cpp`/`whisper.cpp` from source — expect
several minutes depending on your CPU. Later builds hit Docker's layer
cache and are fast. The first *run* additionally downloads the local
models (a few GB total) before `llama-chat`/`llama-embed` report ready in
the logs — normal, one-time, only on a completely fresh
`LLAMA_HF_CACHE`.

Check everything came up:

```bash
docker compose ps
```

All four containers (`bosun`, `llama-chat`, `llama-embed`, `whisper-stt`)
should show `Up`.

## 5. Open it

```
http://localhost:8080
```

Ask it anything — try one of the quick chips (Weather, Fridge, GPS,
System). Everything's mocked out of the box for sensors you don't
actually have (see `config.yaml`'s `sensors:` section) — GPS and weather
still work as real, non-mocked lookups (a location string or your
device's own GPS fix; weather calls the free Open-Meteo API, no key
needed).

## What's next

- **Reach it from your phone, not just `localhost`** — set `web.bind` to
  your machine's LAN IP in `config.yaml` (or `0.0.0.0` if that IP isn't
  fixed), then `docker compose up -d` again. See `README.md`'s Quick
  Start note on this, and `docs/tls.md` for HTTPS with no browser warning.
- **Remote access from anywhere, not just your LAN** — `docs/cloudflare.md`.
- **Real sensors** (GPS receiver, weather, fridge probe) — each sensor's
  `type` in `config.yaml` switches it from `mock` to a real backend; see
  `internal/tools/*.go` and `README.md`'s Configuration Reference for what
  each one needs.
- **Voice** (press-and-hold 🎤, spoken replies) — `whisper-stt` is already
  running; wiring up Piper TTS for spoken replies needs one more model
  file — `docs/voice.md`.
- **A different local model** — swap `llama-chat`'s `command:` in
  `docker-compose.yml` for any `-hf user/repo:quant` llama.cpp supports;
  matching `config.yaml.example`'s comments there explain what to check
  (tool-call format, streaming UTF-8) if you do.
- **The full picture** — `docs/README.md` indexes every deep-dive doc by
  topic; `README.md`'s Architecture section and `SPEC.md`'s roadmap cover
  the rest.

## Troubleshooting

- **A container immediately exits** — `docker compose logs <name>` almost
  always names the missing thing directly (a bad path in `.env`, a port
  already in use). All services use `network_mode: host` (see
  `docker-compose.yml`'s comment on why), so a port conflict means
  something else on the host is already using it.
- **`docker: permission denied`** — you're not in the `docker` group yet,
  or haven't logged back in since being added to it (step 1).
- **First reply takes minutes** — expected on weak/CPU-only hardware; a
  ~2B local model at a few tokens/second can genuinely take that long for
  a tool-using answer. `config.yaml`'s `llm.local.timeout` (default 300s)
  is already sized for this.
