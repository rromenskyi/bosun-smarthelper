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
anything below (`tiny`/`base` Whisper models and a Piper voice model are
all small), but it does mean model choices should stay conservative — no
room here to casually size up "for quality" the way you could on a 16 GB
box.

**Revision after discussion: no Python anywhere in the voice stack, not
just for STT.** The original spec picked Silero for TTS, which is a
`torch.jit`/`.pt` model with no official C++/ONNX runtime — keeping it
would mean keeping Python (and a full `torch` CPU wheel) around just for
TTS. Swapped in **Piper** instead (pure C++ + ONNX Runtime, a
self-contained binary, real Russian voices already published) — it was
already in the original spec as the "add later without rewriting the
API" fallback; this just promotes it to primary, and the fallback slot
Piper vacates is Silero itself (or RHVoice), if quality ever demands it
enough to accept Python. Trade-off worth being upfront about: Piper's
voices are generally a bit more "robotic"/less expressive than Silero's,
and it's single-speaker-per-model rather than Silero's named multi-speaker
list — a real listen-test once a voice is downloaded is the way to judge
whether that trade-off is acceptable, not a documentation guess.

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
                         │        2. STTEngine.Transcribe(wav)  ────┼─ HTTP ─▶ whisper-server
                         │        3. agent.Agent.Ask(text)  (same   │          (whisper.cpp,
                         │           path as text chat)             │          port 1236,
                         │        4. TTSEngine.Synthesize(reply) ───┼─ exec ─▶ loopback only)
                         │           (runs `piper` as a subprocess, │
                         │            no network hop, no sidecar)   │        piper binary,
                         │        5. store WAV in-memory, return    │        no Python, no
                         │           JSON + /api/audio/{id}         │        separate process
                         └───────────────────────────────────────────┘
```

No Python anywhere in this diagram: whisper.cpp is C++, Piper is C++ +
ONNX Runtime, orchestration is Go. TTS doesn't even need a network hop —
see below.

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

## TTS: Piper, shelled out to directly — no server, no Python

[Piper](https://github.com/rhasspy/piper) is a C++ engine using ONNX
Runtime for the neural vocoder and `espeak-ng` (a C library, also no
Python) for grapheme-to-phoneme conversion. ONNX Runtime's CPU execution
provider does runtime CPU-feature dispatch (comparable in spirit to
`ggml`'s `GGML_CPU_ALL_VARIANTS`).

**Important correction, caught before implementation**: `rhasspy/piper`
(the original repo, linked above) is **archived** on GitHub — last push
Aug 2025, 419 open issues, no further activity. Don't build against it.
The actively maintained continuation is
**[OHF-Voice/piper1-gpl](https://github.com/OHF-Voice/piper1-gpl)**
(backed by the Open Home Foundation, the org behind Home Assistant;
5174 stars, last pushed days before this doc was updated, current
release `v1.7.0`) — target this fork instead. It's the same engine
lineage (same voice model format, same author-adjacent ecosystem), just
under active maintenance.

**Verified on this actual host, twice, both without any AVX2-related
crash**:
1. The archived repo's last-ever release (`2023.11.14-2`,
   bundled `libonnxruntime.so.1.14.1`) — ran two Russian voices
   (`ru_RU-denis-medium`, `ru_RU-ruslan-medium`), no crash, no `dmesg`
   illegal-instruction trap. Measured numbers, cold process each time (no
   persistent server, matching the design below):

   | | denis, ~4.0s of audio | ruslan, ~1.8s of audio |
   |---|---|---|
   | model load | 0.37s | 0.37s |
   | inference | 1.11s | 0.30s |
   | real-time factor | 0.27 | 0.17 |
   | total wall time | ~1.5s | 0.74s |
   | peak RSS | ~126 MB | ~126 MB |

   Comfortably inside the spec's "≤1–2s TTS latency" target even
   including a full cold process start each request. Output is 16-bit
   mono PCM WAV at the voice's native 22050 Hz (confirms the sample-rate
   note below).
2. The **active fork's current code** (`piper1-gpl` v1.7.0, which bundles
   a much newer `onnxruntime` — 1.22.0, not 1.14.1) — verified via
   `pip install piper-tts` in a throwaway venv as a quick check (Python
   here only as a verification harness, not the production path — see
   below): `python3 -m piper --model ru_RU-denis-medium.onnx ...`
   synthesized the same test sentence cleanly, no crash. Confirms the
   newer onnxruntime the maintained fork ships also runs fine on this
   CPU, not just the 2-year-old archived one.

**`libpiper` from-source build: also verified, root cause of the earlier
snag found and fixed.** The ~981 "bad vowel file" errors weren't a
version mismatch or a hardware problem at all — they were a **path-length
bug**: `espeak-ng` (built as a sub-step of `libpiper`'s own CMake build)
hardcodes a 160-byte path buffer on POSIX (`N_PATH_HOME_DEF` in
`speech.h`), and the first build attempt ran inside this session's
scratch directory, whose path is a long, UUID-nested one — long enough
that `snprintf`-ing a data file's full path (base dir + `phsource/vwl_en_us/a`
etc.) silently truncated mid-string, so `LoadSpectSeq` tried to open a
cut-off, nonexistent path and failed. **Rebuilding the exact same source
in a short path (`/opt/piper-build/...`) succeeded cleanly** —
`espeak-ng`, `libpiper.so`, and (unexpectedly useful) a ready-made
`piper_exe` CLI binary all built with zero errors. **Practical
consequence for `deploy/piper/Dockerfile`**: build inside a short path
(Docker's own build context root, e.g. `/src`, is already short — this
should never bite in the real Dockerfile, only in unusually deep
directories like this session's scratch space).

Ran the freshly built `piper_exe` against both downloaded voices —
clean synthesis, no crash, no `dmesg` entries, confirming the from-source
build (not just the Python-wheel verification above) also works on this
CPU:

| | denis | ruslan |
|---|---|---|
| total wall time (cold process) | 2.40s | 1.85s |

Slightly slower than the prebuilt archived binary's numbers above (which
used an older onnxruntime 1.14.1 and a shorter test sentence for one of
the two) — still comfortably usable, though closer to the "≤1–2s" target
than the earlier measurement; worth re-measuring once request text length
and process-reuse strategy are finalized during implementation.

**One real difference to handle**: this build's `piper_exe` outputs
**IEEE float WAV** (`file` reports "WAVE audio, IEEE Float, mono
22050 Hz"), not the 16-bit PCM the archived binary produced. Browser
`<audio>` support for float WAV is less consistent than for 16-bit PCM
(Safari in particular has had issues) — `handleTTS` should pipe
`piper_exe`'s output through `ffmpeg -f wav -acodec pcm_s16le -` (ffmpeg
is already in the plan for STT audio conversion, so this is reusing the
same subprocess, not adding a new dependency) rather than assuming the
browser will play it directly.

**Bonus finding: no custom C++ wrapper needed.** `libpiper`'s own
`src/main/` builds a complete reference CLI (`piper_exe`, source at
`libpiper/src/main/main.cpp`) with exactly the flags needed
(`-m`/`--espeak_data`/`-f`/stdin text). The "~40-line `bosun-piper-cli`
we write" plan below is now simpler than planned — copy the upstream
`piper_exe` binary (or trivially fork/trim it if its stdin/stdout
handling needs adjusting) instead of writing one from scratch against
`piper.h`.

Model loading is fast enough (small ONNX graph, no multi-gigabyte weights)
that **Piper doesn't need to run as a persistent server at all** — Bosun
can shell out to the binary per request, exactly like it already shells
out to `ffmpeg`/`tesseract`/`pdftoppm` for document handling
(`internal/webui/pdf.go`). That removes an entire component from the
architecture (no sidecar process, no Dockerfile, no port to manage, no
HTTP client to write) compared to the Silero design:

```
piper_exe -m /opt/bosun/models/tts/<voice>.onnx \
          --espeak_data /opt/bosun/espeak-ng-data \
          -f - <<< "Капитан! Старпом на связи." | \
  ffmpeg -f wav -i - -acodec pcm_s16le -f wav - > out.wav
```

**No custom C++ wrapper needed** — confirmed empirically (see verification
above): `libpiper`'s own build already produces a complete, working CLI
(`piper_exe`, built from `libpiper/src/main/` alongside the library
itself) with exactly the flags this needs. The original plan here was a
~40-line C++ program written against `piper.h`; that's no longer
necessary — just build and ship the upstream `piper_exe` as-is. The
`ffmpeg` stage after it converts the IEEE-float WAV `piper_exe` emits into
16-bit PCM for consistent browser `<audio>` playback (see verification
notes above) — reusing the same `ffmpeg` subprocess already needed for
STT audio conversion, not a new dependency.

Bosun's `internal/webui`/`internal/voice` code runs this via `os/exec`
with the text piped to stdin and the (ffmpeg-converted) WAV read from
stdout — no temp files needed for the common case, matching "don't
persist raw audio."

**`deploy/piper/Dockerfile`** (new): clone `OHF-Voice/piper1-gpl` pinned
at a specific commit/tag (`v1.7.0`, verified above — confirm still
current when this actually gets written), build `libpiper/` per its own
`CMakeLists.txt` **from a short path** (see the path-length bug above —
Docker's own build context root is already short, so this is a
non-issue in practice, just worth remembering if the build ever moves
into a deeply nested directory) — this downloads/builds `espeak-ng` and
downloads the `onnxruntime` release itself, no apt packages needed for
either — then copy `piper_exe` + `libpiper.so`/`libonnxruntime.so` +
`espeak-ng-data/` into the final image (mirrors
`deploy/llama/Dockerfile`'s two-stage builder/runtime shape). Voice
`.onnx`/`.onnx.json` model files stay a bind-mounted `./data/models/tts/`
(same "plain host directory, survives `docker compose down -v`"
reasoning as `./data/bosun`). No new Docker *service* — this becomes part
of the existing `bosun` image, same as `ffmpeg`/`tesseract` are today.

**Bosun's `POST /api/tts`**: accepts `{"text": "..."}`, runs the
configured Piper voice, returns the WAV, logs latency. **Text passes
through completely unmodified** — no punctuation stripping, no
case-folding — since intonation cues (`!`, `...`, `?`, capitalization) are
the LLM's job to produce and the TTS engine's job to interpret; Bosun's
persona (`agent.SetPersona`, `docs/settings.md`) already controls the
*style* the text arrives in. An SSML/preprocessing hook is worth a single
passthrough no-op function now (`preprocessForTTS(text string) string {
return text }`) so a future normalization step has an obvious place to
live, without building out anything now.

**Voice/speaker naming changes from the original spec**: `aidar`/`eugene`
were Silero-specific multi-speaker names — Piper voices are one model
file per voice, not one model with named speakers inside it.
**Confirmed against the live `rhasspy/piper-voices` Hugging Face repo**
(not just recalled) — note this is a separate, still-actively-updated
model-hosting repo (last modified days before this doc was updated) from
the archived `rhasspy/piper` *engine* repo above; voice models didn't go
stale just because the original engine did: `ru/ru_RU/` currently has
four voices — `denis`, `dmitri`, `ruslan` (male) and `irina` (female),
each only in a `medium` quality tier. Downloaded and test-synthesized
`denis` and `ruslan` above; a real listen-test (not yet done — needs an
actual speaker, not a shell) between those two, and maybe `dmitri` as a
third candidate, decides the default.
There's no separate `speaker` field in config any more —
`voice.tts.model_path` (below) *is* the voice selection, since each Piper
voice is its own model file; switching voices is a one-line config edit,
no `speaker` parameter needed per request.

**Sample rate is dictated by the voice model, not freely chosen**: unlike
Silero (where 24 kHz was a real choice), a Piper voice's `.onnx.json`
sidecar file specifies its own native sample rate (commonly 22.05 kHz for
existing Piper voices) — `config.yaml`'s `sample_rate` becomes informational
(what to expect / log), not a knob that forces resampling, unless a
concrete reason to resample shows up later.

## `STTEngine`/`TTSEngine` adapters (plain Go interfaces)

Since orchestration lives in the Go backend (see architecture decision
above), the adapter interfaces belong there too — `internal/voice` would
define them, with an HTTP-client implementation for STT and a
subprocess-exec implementation for TTS as the first (and for now, only)
implementations:

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
    Synthesize(ctx context.Context, text string) ([]byte, error)
}
```

`WhisperCppSTT` (an HTTP client against `whisper-server`) and `PiperTTS`
(an `os/exec` wrapper around the `piper` binary) are the first
implementations — neither needs to know the other exists, and the
`TTSEngine` interface doesn't care that one implementation happens to shell
out instead of making an HTTP call. This is exactly where the interface
split already pays for itself: if Piper's quality turns out not to be
good enough, a future `SileroTTS` (HTTP client to a Python sidecar, as
originally designed) or `RHVoiceTTS` implements the same interface with
zero changes to `internal/webui`'s voice handlers.

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
already looks. STT's model path/thread count still belong to the
`whisper-server` process (`docker-compose.yml`'s `command:`, exactly like
`llama-chat`'s `-t 4`) — Bosun only needs to know *where* to reach it,
mirroring how `llm.embeddings.base_url` already points at a separate
`llama-server` instance. TTS has no separate process to point at anymore,
so its config is the binary/model paths directly (still never hardcoded
in Go source, per the spec's own requirement):

```yaml
voice:
  stt:
    base_url: "http://127.0.0.1:1236"   # whisper-server
    language: "ru"
    auto_detect_language: false          # true lets whisper.cpp detect instead
  tts:
    binary_path: "/usr/local/bin/piper"
    model_path: "/opt/bosun/models/tts/ru_RU-<voice>-medium.onnx"
    sample_rate: 22050                    # informational — set by the voice model, not a resample target
  vad:
    enabled: true          # ignored until conversation mode exists
    silence_ms: 900
  audio:
    source: "default"      # ignored until conversation mode exists
    sink: "default"
```

Empty `voice.stt.base_url` or empty `voice.tts.model_path` (the default)
disables that half of the feature — `/api/voice` etc. report
`{"enabled": false}`, same pattern `docs/settings.md`'s settings store and
`docs/memo-search.md`'s document store already use for an optional
feature with no backing service configured.

## Deployment: Docker, not systemd, for the MVP

The original spec suggested systemd because it assumed a service that
directly touches audio hardware. Since the MVP's STT engine is a pure
HTTP inference server (no audio device access — see above) and TTS is now
just a subprocess call from within Bosun's own container, everything
fits the **existing** Docker Compose stack: one new `whisper-stt` service
(mirroring `llama-chat`/`llama-embed`'s `network_mode: host`,
`restart: unless-stopped`) plus `espeak-ng`/`piper`/voice-model additions
to the existing `bosun` image — reboot survival is already solved by
Docker's restart policy, no new systemd units needed at this phase.
Systemd only becomes the right tool once conversation mode's
host-audio-touching component exists (see above).

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
`agent.Agent` as text chat) replies → Piper speaks the reply — over the
existing web UI, phone or desktop, fully offline once models are
downloaded, no Python anywhere in the path.

## Open questions before implementation starts

1. ~~Verify ONNX Runtime actually runs on this CPU without AVX2~~ —
   **resolved**: confirmed twice on this actual host, once with the
   archived repo's last release (onnxruntime 1.14.1) and once with the
   actively maintained fork's current code (onnxruntime 1.22.0, via
   `pip install piper-tts` as a quick check). Both synthesized real
   Russian audio with no crash. See the TTS section above for numbers.
2. ~~Fix the `libpiper` from-source build~~ — **resolved**: root cause was
   a path-length bug in `espeak-ng`'s hardcoded 160-byte path buffer,
   triggered by building inside this session's deeply nested scratch
   directory, not a version mismatch or hardware issue. Rebuilding in a
   short path (`/opt/piper-build/...`) succeeded cleanly and produced a
   working `piper_exe` CLI. See the TTS section above for the fix and the
   practical note for `deploy/piper/Dockerfile` (build context roots are
   already short, so this shouldn't recur in practice).
3. **Pick the actual Piper voice model by ear** — `denis` and `ruslan` are
   both downloaded and confirmed to synthesize correctly (verified twice:
   the archived prebuilt binary and the from-source `piper_exe` build);
   still needs an actual listen-test (by ear, not by shell exit code) to
   pick a default, optionally trying `dmitri` as a third candidate.
4. **Confirm the IEEE-float-to-PCM16 `ffmpeg` conversion step actually
   sounds right** — `piper_exe` emits float WAV, converted to PCM16 for
   browser compatibility (see TTS section); the conversion itself is
   trivial but hasn't been listen-tested for artifacts.
5. **Pin an exact `piper1-gpl` commit/tag** for `deploy/piper/Dockerfile`,
   the same way `LLAMA_CPP_REF` pins `deploy/llama/Dockerfile` — `v1.7.0`
   is what got tested above, but confirm it's still current when the
   Dockerfile actually gets written.
6. **`whisper-server`'s actual request/response shape** needs verifying
   against the pinned `whisper.cpp` commit before writing `handleSTT` —
   the endpoint path and JSON field names in this doc are from
   whisper.cpp's typical `server` example, not yet confirmed against the
   exact pinned build.
7. **ffmpeg availability** — `internal/webui`'s Dockerfile already ships
   `poppler-utils`/`tesseract-ocr`; add `ffmpeg` there too once this lands
   (`espeak-ng` no longer needs a separate apt package — `libpiper`'s own
   build downloads/builds it from source, see the TTS section above).
