# Piper TTS build assets

`wav-pcm16.patch` is used by the root `Dockerfile`'s `piper-builder`
stage — not a separate Dockerfile of its own (an earlier plan; superseded
once building `bosun`'s actual Alpine/musl image surfaced a deeper
blocker than TTS output format — see `docs/voice.md`'s TTS section for
the full story).

The patch fixes `OHF-Voice/piper1-gpl`'s reference `piper_exe` CLI (built
from `libpiper/src/main/`) to emit 16-bit PCM WAV instead of its default
IEEE float WAV, so no `ffmpeg` conversion pass is needed per TTS request.
Verified against `piper1-gpl` `v1.7.0` on this host, both standalone and
inside the deployed container.
