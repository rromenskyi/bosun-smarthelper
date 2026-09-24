# Voice devices

Bosun can be driven by small hardware voice front-ends — a speaker with a
microphone, like the ESP32-S3 speaker firmware at
[rromenskyi/esp32-speaker](https://github.com/rromenskyi/esp32-speaker). The
device only captures and plays audio; Bosun does everything else with the same
engines as the web UI's voice button: whisper STT (remote-preferred, local
fallback), the agent with its tools and memory, and Piper TTS.

## Enabling

```yaml
voice:
  tts: { ... }            # required
  stt: { ... }            # required
  devices:
    enabled: true
    token_env: "BOSUN_DEVICE_TOKEN"   # optional; empty = trusted LAN, no token
    max_utterance_seconds: 30
    response_hint: "This is a spoken conversation through a speaker: ..."  # default shown below
```

With `token_env` set, put the token in `.env` and configure the same token on
the device. The endpoint is `GET /api/device` on the web UI's address, e.g.
`ws://<host>:<http_fallback_port>/api/device` from a device on the LAN (plain
WebSocket on the HTTP fallback listener avoids TLS on the device).

## Protocol

One WebSocket per device, defined in the firmware repo's
[`docs/PROTOCOL.md`](https://github.com/rromenskyi/esp32-speaker/blob/main/docs/PROTOCOL.md):
JSON text frames for control, binary frames of 20 ms mono 16-bit PCM at 16 kHz
for audio. A turn:

1. device: `listen start`, microphone frames, `listen stop`;
2. Bosun: `thinking`, transcribes, and asks the agent with a streaming call;
3. as each sentence of the answer completes, it is synthesized and streamed
   (`speak start` before the first one, reply frames faster than real time —
   the device buffers and pushes back over TCP — and `speak stop` after the
   last), so the first sentence plays while the model is still writing.

Device turns add `response_hint` to the system prompt (default: answer in one
to three short sentences of plain speech, no lists/links/markdown): a chat-style
reply is slow to generate, slow to synthesize on weak hardware and tiring to
listen to. It costs ~35 prompt tokens on the local model; `""` disables it.
Markdown is stripped before TTS either way, as for the web UI's 🔊.

An `abort` from the device (the user interrupted) cancels the turn in flight.
Utterances shorter than 250 ms are answered with an `error` (a mis-press).

## Sessions

Each device gets one stable chat session, `device-<device name>`, so a
conversation continues across reconnects and appears in the web UI's session
list like any other chat. Local-model turns queue behind web chat exactly like
a second browser tab would.

## Logs

Every turn logs `device turn` with the recognized text and timings: `stt_ms`,
`first_audio_ms` (listen stop → first reply audio sent), `total_ms`, the number
of spoken chunks and `reply_ms` (length of the spoken reply).

## Not yet

- Overlapping synthesis of the next sentence with sending the current one
  (Piper is still one process per sentence).
- Announcing alerts on devices (the `speaker` alert channel still plays on the
  host).
- Opus audio.
