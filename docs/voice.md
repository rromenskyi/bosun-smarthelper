# Voice interface (design spec — not yet implemented)

Goal: a fully local, offline-capable voice front-end to the existing chat
loop — `microphone → STT → the same agent.Agent used by web chat → TTS →
speaker` — on this host (a 2011 Mac Mini, Intel Sandy Bridge i5, no
AVX2/FMA/F16C; confirmed via `/proc/cpuinfo`: has `avx sse4_1 sse4_2`,
nothing newer). MVP scope: press a button, speak, hear Bosun answer.
Continuous "conversation mode" and a dedicated Bluetooth speaker are
explicitly follow-up work, not part of this pass.

**Correction to the original hardware assumption**: this host has **8 GB**
RAM (`2× 4 GB`, confirmed via `dmidecode`), not 16 GB. Doesn't block
anything below (`tiny`/`base` Whisper models and Silero's `.pt` are both
small), but it does mean model choices should stay conservative — no room
here to casually size up "for quality" the way you could on a 16 GB box.

## Architecture decision: no separate "voice service" process

The original sketch drew STT/TTS/voice-orchestration as a distinct service
box, separate from "existing Bosun backend." Mapped onto this actual
codebase, that adds a process and a network hop for no benefit — the
existing `internal/webui.Server` (`internal/webui/server.go`) already owns
the agent, sessions, and every other HTTP surface. **Recommendation: add
`/api/stt`, `/api/tts`, `/api/voice`, `/api/audio/{id}`, `/api/audio-devices`
as new routes on this same server**, calling the existing in-process
`agent.Agent` directly (same call `handleChat` already makes) instead of
looping back through its own HTTP API. STT and TTS themselves still run as
**separate inference processes** — see below — so nothing about isolating
those engines is lost; only the *orchestration* layer collapses into code
that already exists.

```
                         Bosun (internal/webui.Server, unchanged process)
                         ┌─────────────────────────────────────────┐
 browser ── POST /api/voice ──▶  handleVoice                        │
 (MediaRecorder)         │        1. ffmpeg: webm/opus → 16k mono WAV
                         │        2. STTEngine.Transcribe(wav)      │
                         │        3. agent.Agent.Ask(text)  (same   │
                         │           path as text chat)             │
                         │        4. TTSEngine.Synthesize(reply)    │
                         │        5. store WAV in-memory, return    │
                         │           JSON + /api/audio/{id}         │
                         └───────────┬──────────────────┬──────────┘
                                     │ HTTP                │ HTTP
                                     ▼                      ▼
                          whisper-server (whisper.cpp)   silero-tts-service
                          port 1236, loopback only        (Python sidecar)
                                                           port 1237, loopback only
```

## STT: whisper.cpp, run as its own server (mirrors `llama-server`)

whisper.cpp ships its own HTTP server example (`whisper-server`), and it's
built on the same `ggml` backend as `llama.cpp`. That means the exact
CPU-dispatch trick already used in `deploy/llama/Dockerfile` — build with
`-DGGML_NATIVE=OFF -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON` so the
binary picks the right SIMD variant (SSE4.2/AVX, never AVX2/FMA/F16C) at
startup instead of assuming one — applies unchanged. No Python, no
PyTorch, matching the requirement directly.

**`deploy/whisper/Dockerfile`** (new, same shape as `deploy/llama/Dockerfile`):
```dockerfile
ARG WHISPER_CPP_REF=<pin to a specific commit, like LLAMA_CPP_REF>
FROM ubuntu:26.04 AS builder
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake git ca-certificates libssl-dev pkg-config curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone https://github.com/ggml-org/whisper.cpp.git . && git checkout "${WHISPER_CPP_REF}"
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release \
    -DGGML_NATIVE=OFF -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON \
    -DWHISPER_SDL2=OFF && cmake --build build -j"$(nproc)" --target whisper-server
FROM ubuntu:26.04
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libssl3 libgomp1 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /src/build/bin/ /usr/local/lib/whisper.cpp/
RUN ln -s /usr/local/lib/whisper.cpp/whisper-server /usr/local/bin/whisper-server
ENV LD_LIBRARY_PATH=/usr/local/lib/whisper.cpp
USER ubuntu
ENTRYPOINT ["/usr/local/bin/whisper-server"]
```

**`docker-compose.yml`** addition, mirroring `llama-chat`/`llama-embed`:
```yaml
whisper-stt:
  build: ./deploy/whisper
  image: bosun-whisper-cpp:local
  container_name: whisper-stt
  restart: unless-stopped
  network_mode: host
  volumes:
    - ./data/models/whisper:/models:ro
  command: >-
    --model /models/ggml-tiny.bin
    --host 127.0.0.1 --port 1236
    --threads 4 --language ru
```

Model files (`ggml-tiny.bin`, later `ggml-base.bin`) download once with
`whisper.cpp`'s own `models/download-ggml-model.sh` into
`./data/models/whisper/` (a plain bind-mounted host directory, same
survives-`docker compose down -v` reasoning as `./data/bosun`). Switching
tiny → base is a one-line `command:` edit, no rebuild.

**Bosun's `POST /api/stt`** (new handler, `internal/webui/voice.go`):
accepts `audio/wav` (already-converted PCM), proxies to
`http://127.0.0.1:1236/inference` (whisper-server's actual endpoint),
reshapes the response into the contracted shape, and logs latency:
```json
{"text": "Старпом, как наши системы?", "language": "ru", "duration_ms": 1840}
```
`duration_ms` is *this request's* STT latency (matches the spec's example
key name, even though it reads like audio duration at first glance — kept
as specified since the voice pipeline's `timings` object already has a
separate, unambiguous `stt_ms`).

## Converting browser audio: ffmpeg, server-side

Browser `MediaRecorder` output (`audio/webm;codecs=opus` on Chrome,
`audio/mp4` on Safari) isn't what whisper.cpp wants (16 kHz mono PCM WAV).
Converting in the browser would mean hand-rolling a WAV encoder in JS —
exactly the complexity the spec says to avoid. Shell out to `ffmpeg`
instead, the same pattern already used for PDF page rendering
(`internal/webui/pdf.go`'s `pdftoppm`/`tesseract` calls):
```
ffmpeg -i <uploaded blob> -ar 16000 -ac 1 -f wav -
```
via a subprocess with stdin/stdout pipes — no temp files needed, matching
"don't save raw audio persistently."

## VAD

**MVP needs none.** Push-to-talk means the browser itself decides
start/stop (`touchstart`/`mousedown` → `touchend`/`mouseup`), so there's no
silence to detect — the whole VAD section of the original spec only
applies to **conversation mode**, explicitly deferred past the MVP.

When conversation mode is built: silence-based endpointing (RMS/energy
threshold over the incoming audio stream, `silence_ms` configurable,
defaulting to 900ms per the original spec) runs *inside the voice
service's audio-capture loop*, not as a separate component — there's
nothing to design further here until that phase starts.

## TTS: Silero, run as a small Python sidecar

Silero ships as a `torch.jit`-traced `.pt` module — there's no C++/ggml
equivalent the way there is for STT, so some Python is genuinely necessary
here (the original spec's own ban on Python/PyTorch was scoped to STT
specifically, and its own config sample already names a `.pt` file). Keep
it contained to one small, swappable process — not folded into Bosun's own
codebase — so the `TTSEngine` interface (below) never has to know it's
Python underneath, and swapping in Piper/RHVoice later is a sidecar swap,
not a Bosun code change.

**`deploy/silero-tts/`** (new): a ~40-line FastAPI app —
```python
# server.py
import torch
from fastapi import FastAPI, Response
from pydantic import BaseModel

app = FastAPI()
model = torch.package.PackageImporter(MODEL_PATH).load_pickle("tts_models", "model")
model.to("cpu")

class SynthesizeRequest(BaseModel):
    text: str
    speaker: str = "aidar"

@app.post("/synthesize")
def synthesize(req: SynthesizeRequest):
    audio = model.apply_tts(text=req.text, speaker=req.speaker, sample_rate=SAMPLE_RATE)
    return Response(content=wav_bytes(audio, SAMPLE_RATE), media_type="audio/wav")
```
(`MODEL_PATH`/`SAMPLE_RATE` from env vars — no hardcoded paths, per the
spec's own requirement.) Runs via `pip install torch --index-url
https://download.pytorch.org/whl/cpu` (CPU-only wheel — no CUDA pulled
in) plus `fastapi`/`uvicorn`. Model loads once at process startup, stays
resident (matches "CPU-only and fully offline after the initial model
download").

**`docker-compose.yml`** addition:
```yaml
silero-tts:
  build: ./deploy/silero-tts
  image: bosun-silero-tts:local
  container_name: silero-tts
  restart: unless-stopped
  network_mode: host
  environment:
    - MODEL_PATH=/models/v5_5_ru.pt
    - SAMPLE_RATE=24000
  volumes:
    - ./data/models/tts:/models:ro
  command: >-
    uvicorn server:app --host 127.0.0.1 --port 1237
```

**Bosun's `POST /api/tts`**: accepts `{"text": "...", "speaker": "aidar"}`,
proxies to `http://127.0.0.1:1237/synthesize`, streams the WAV back,
logs latency. **Text passes through completely unmodified** — no
punctuation stripping, no case-folding — since intonation cues (`!`,
`...`, `?`, capitalization) are the LLM's job to produce and Silero's job
to interpret; Bosun's persona (`agent.SetPersona`, `docs/settings.md`)
already controls the *style* the text arrives in. An SSML/preprocessing
hook is worth a single passthrough no-op function now
(`preprocessForTTS(text string) string { return text }`) so a future
normalization step has an obvious place to live, without building out
anything now.

Two speakers to validate once the sidecar exists: `aidar`, `eugene` (both
male, per the spec) — `speaker` is a per-request field, defaulting to
whatever `config.yaml`'s `voice.tts.speaker` says.

## `STTEngine`/`TTSEngine` adapters (Go interfaces, not Python)

Since orchestration lives in the Go backend (see architecture decision
above), the adapter interfaces belong there too — plain Go interfaces
`internal/voice` would define, with the current whisper-server/Silero
sidecar clients as the first (and for now, only) implementations:

```go
// internal/voice/voice.go
type Transcript struct {
    Text     string
    Language string
}

type STTEngine interface {
    Transcribe(ctx context.Context, wav []byte) (Transcript, error)
}

type TTSEngine interface {
    Synthesize(ctx context.Context, text, speaker string) ([]byte, error)
}
```

`WhisperCppSTT` and `SileroTTS` (both thin HTTP clients against their
respective sidecars) are the first implementations. A future `PiperTTS` or
`RHVoiceTTS` implements the same `TTSEngine` interface — `internal/webui`'s
voice handlers never change.

## `POST /api/voice`: the full pipeline

```json
{
  "recognized_text": "Как наши системы?",
  "response_text": "Капитан! Все системы в норме.",
  "audio_url": "/api/audio/8dca31.wav",
  "timings": {"stt_ms": 1300, "llm_ms": 2100, "tts_ms": 700, "total_ms": 4100}
}
```
`audio_url` points at an **in-memory**, short-TTL (a few minutes) store
keyed by a random ID — same "don't persist raw audio" rule as recordings;
a `GET /api/audio/{id}` miss (expired or bogus ID) is a plain 404.

This endpoint reuses whatever session the request carries (same
`session_id` cookie/param `handleChat` already reads) so a voice turn and
a typed turn share one conversation history — voice is a second front-end
to the same loop, not a parallel assistant.

## Web UI: push-to-talk button

Big button, `#voice-toggle`-style element next to the existing text input
(same file, `internal/webui/index.html`, same vanilla-JS-no-framework
approach already used for the settings/documents dialogs). States drive a
CSS class (`.voice-idle` / `.voice-listening` / `.voice-recognizing` /
`.voice-thinking` / `.voice-speaking` / `.voice-error`), not new markup per
state:

```js
button.addEventListener('touchstart', startRecording);
button.addEventListener('mousedown', startRecording);
button.addEventListener('touchend', stopRecordingAndSend);
button.addEventListener('mouseup', stopRecordingAndSend);
```
`startRecording` opens a `MediaRecorder` on `navigator.mediaDevices.getUserMedia({audio: true})`;
`stopRecordingAndSend` stops it, `POST /api/voice` with the recorded blob
as multipart body, then on response: render the recognized text and the
reply text as normal chat bubbles (reusing `addMessage`, exactly like a
typed turn), and autoplay the returned WAV via a plain `<audio>` element.

## Linux audio device configuration (conversation-mode phase only)

**Not needed for the push-to-talk MVP** — recording and playback both
happen in the browser, so Bosun's own process never touches ALSA/PipeWire.
This section only matters once conversation mode adds a standalone
microphone/speaker loop running directly on the host:

- `voice.audio.source`/`voice.audio.sink` (config, default `"default"`)
  select a PipeWire/Pulse device by name; `pactl list short sources|sinks`
  (or `wpctl status`) enumerates what's available. A future `GET
  /api/audio-devices` route can just shell out to `pactl` and return the
  parsed list, so the settings page could eventually offer a dropdown —
  not built now, just noted as the natural extension point.
- Playback via `pw-play <file>` (or `paplay` as the PulseAudio-compat
  fallback) — a subprocess call, same shelling-out pattern as `ffmpeg`/
  `tesseract` elsewhere in this codebase.
- Whichever process does this (a new `bosun-voice-loop` component, not yet
  designed in detail) needs real audio hardware access, which is exactly
  where **not** running in Docker becomes the right call — a systemd unit
  running directly on the host, alongside the existing Docker Compose
  stack, is the natural fit. Deferred until conversation mode is actually
  being built.

## Config: extending `config.yaml`

New top-level `voice:` block, `internal/config/config.go` gets a
`VoiceConfig` struct (`mapstructure:"voice"`) the same way `WebConfig`
already looks. Model **paths and thread counts belong to the sidecar
processes** (`docker-compose.yml`'s `command:`/`environment:`, exactly
like `llama-chat`'s `-t 4` and `LLAMA_API_KEY`), not to Bosun's own
config — Bosun only needs to know *where* to reach them, mirroring how
`llm.embeddings.base_url` already points at a separate `llama-server`
instance rather than Bosun loading an embedding model itself:

```yaml
voice:
  stt:
    base_url: "http://127.0.0.1:1236"   # whisper-server
    language: "ru"
    auto_detect_language: false          # true lets whisper.cpp detect instead
  tts:
    base_url: "http://127.0.0.1:1237"   # silero-tts sidecar
    speaker: "aidar"
    sample_rate: 24000
  vad:
    enabled: true          # ignored until conversation mode exists
    silence_ms: 900
  audio:
    source: "default"      # ignored until conversation mode exists
    sink: "default"
```

Empty `voice.stt.base_url`/`voice.tts.base_url` (the default) disables the
feature entirely — `/api/voice` etc. report `{"enabled": false}`, same
pattern `docs/settings.md`'s settings store and `docs/memo-search.md`'s
document store already use for an optional feature with no backing
service configured.

## Deployment: Docker, not systemd, for the MVP

The original spec suggested systemd because it assumed a service that
directly touches audio hardware. Since the MVP's STT/TTS engines are pure
HTTP inference servers (no audio device access — see above), they fit the
**existing** Docker Compose stack exactly like `llama-chat`/`llama-embed`
already do: `whisper-stt` and `silero-tts` services, `network_mode: host`,
`restart: unless-stopped` — reboot survival is already solved by Docker's
restart policy, no new systemd units needed at this phase. Systemd only
becomes the right tool once conversation mode's host-audio-touching
component exists (see above).

## Logging

Per request: audio duration, STT latency, recognized text, LLM latency,
response text, TTS latency, total latency, any error — all via the
existing `slog.Logger` already threaded through `internal/webui.Server`,
same as every other handler. Raw audio is never written to disk in normal
operation; a `debug_mode` toggle (config, default off) can keep the last
few recordings in a bounded ring buffer under `data/bosun/voice-debug/`
for troubleshooting, matching the "don't persist raw audio" instruction
while still leaving a debugging escape hatch.

## Explicit non-goals (unchanged from the original spec)

Wake word, Bluetooth pairing automation, voice cloning, speaker ID,
streaming token-by-token TTS, complex DSP/echo cancellation, Kubernetes,
cloud STT/TTS, a native mobile app — none of this now.

## MVP acceptance criteria

Press the button → speak → whisper.cpp recognizes → Bosun (same
`agent.Agent` as text chat) replies → Silero speaks the reply — over the
existing web UI, phone or desktop, fully offline once models are
downloaded.

## Open questions before implementation starts

1. **Model source/licensing check for `v5_5_ru`** — confirm the exact
   Silero release URL/version to pin (analogous to `LLAMA_CPP_REF`), so
   the model download step is reproducible, not "whatever's newest."
2. **`whisper-server`'s actual request/response shape** needs verifying
   against the pinned `whisper.cpp` commit before writing `handleSTT` —
   the endpoint path and JSON field names in this doc are from
   whisper.cpp's typical `server` example, not yet confirmed against the
   exact pinned build.
3. **ffmpeg availability** — `internal/webui`'s Dockerfile already ships
   `poppler-utils`/`tesseract-ocr`; add `ffmpeg` there too once this lands.
