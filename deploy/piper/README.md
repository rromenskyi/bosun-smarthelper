# Piper TTS build assets

Not yet wired into `docker-compose.yml` — staged here ahead of the voice
interface implementation (`docs/voice.md`).

`wav-pcm16.patch` fixes `OHF-Voice/piper1-gpl`'s reference `piper_exe` CLI
(built from `libpiper/src/main/`) to emit 16-bit PCM WAV instead of its
default IEEE float WAV, so no `ffmpeg` conversion pass is needed per TTS
request. Verified against `piper1-gpl` `v1.7.0` on this host — see
`docs/voice.md`'s TTS section for how it was tested. Apply with:

```
git apply /path/to/deploy/piper/wav-pcm16.patch
```

from the root of a `piper1-gpl` checkout, before building `libpiper/`.
