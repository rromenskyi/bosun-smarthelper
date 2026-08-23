# Cameras: live view + archive

A 📹 button in the web UI for one or more WiFi cameras — live MJPG view
and browsing/playing recorded segments. A real Bosun feature (config,
web UI, HTTP API), unlike this project's first pass at this
(`dashcam`, a standalone `docker-compose.yml` service pointed straight
at one camera) — that approach hit a hard wall the moment a human also
wanted to watch the live feed: **the camera's own web server accepts
only one client**, and the recorder was already using it.

## Why a relay, and why it lives inside `bosun` itself

`internal/cameras.Relay` is the *only* thing that ever opens a
connection to a camera's `stream_url` directly. It parses the upstream
`multipart/x-mixed-replace` MJPG stream and republishes every frame to
however many subscribers currently want it — the archive recorder, and
any number of live browser tabs, none of which ever touch the camera
directly. This is what "only one client" actually requires: without a
relay, the recorder and a live viewer would fight over that one slot.

The relay runs inside the `bosun` process, not a separate container:
`bosun` already has `ffmpeg` installed (`Dockerfile`, used today for
voice-input conversion) and an HTTP server with exactly the "expose a
subsystem as a button + API" shape this needed already (the monitoring
dashboard, backups, alerts all follow the same pattern).

A real, well-tested MJPG/RTSP restreamer exists (go2rtc) and would also
solve this. Not used here for the same reason this project hand-rolled
S3 signing instead of pulling in the AWS SDK, and the sandbox's
Docker-CLI wrapper instead of the Docker Go SDK: MJPEG multipart
relaying is a small, well-defined protocol, not something that benefits
from a second container with its own config format and attack surface.

**Known limitation, accepted for now**: raw MJPG via `<img src>` doesn't
render in Safari/iOS (no `multipart/x-mixed-replace` support). If
iPhone live-viewing becomes a real requirement, that's a codec/protocol
change (HLS) layered on top of the same relay core, not an architecture
change.

## Config

```yaml
cameras:
  - name: "front"              # url-safe id — API paths, archive directory
    label_ru: "Нос"
    label_en: "Bow"
    stream_url: "http://192.168.1.50:81/stream"
    record: true                # cyclic archival; false = live view only
    segment_seconds: 300
    segment_count: 50
```

Config-only, not settings-page-editable — like `metrics.sources` and
`alerts.channels`, which cameras exist is an infrastructure decision, not
a phone-UI one. An arbitrary number of cameras is supported; each gets
its own `Relay` and (if `record: true`) its own recorder subprocess.

## Recording: an internal consumer of the relay, not of the camera

For each camera with `record: true`,
`cmd/smarthelper/main.go`'s `runCameraRecorder` shells out to `ffmpeg`
pointed at **the relay's own endpoint**
(`http://127.0.0.1:<port>/api/cameras/<name>/stream`), never the camera —
the camera's one client slot stays reserved for the relay. `<port>` is
resolved once at startup from `web.http_fallback_bind` if TLS is
configured, else `web.bind` directly (already plain HTTP when TLS isn't
set at all) — always plain HTTP for this loopback-only call, since
giving `ffmpeg` a cert to validate would be pure overhead for a
connection that never leaves the host. If `ffmpeg` exits (the relay
restarting, a transient hiccup), the recorder waits a few seconds and
starts it again.

The actual `ffmpeg` invocation — `-f segment -segment_time
-segment_wrap -reset_timestamps`, `-c:v libx264 -preset veryfast -crf
23`, `-an` (no audio) — is exactly what the original `dashcam` service
used, proven live against a real camera before this rewrite: measured
directly, both a pure remux (`-c:v copy`) and the full libx264 encode
ran at the *same* ~0.32x-realtime speed, meaning the camera's own frame
delivery (~8 fps despite an advertised 25) is the bottleneck, not this
host's CPU — so the smaller libx264 output costs nothing extra. Segments
land in `./data/dashcam/<name>/cam_%03d.mp4` on the host (a sibling of
`./data/bosun`, bind-mounted into `bosun` alongside it) —
`internal/backup.BuildArchive` only ever walks `cfg.Backup.DataDir`
(`./data/bosun`), so this is excluded from the S3 backup by
construction, not a manual exclude list. `-segment_wrap` is what makes
this genuinely cyclic: `ffmpeg` itself overwrites the oldest segment
once full, no separate cleanup job.

## Web UI

The 📹 button (hidden until `GET /api/cameras/list` reports at least one
camera) opens a dialog: a camera picker if more than one is configured,
a live `<img src="/api/cameras/<name>/stream">` (browsers render MJPG
multipart natively, no player library), and a list of recorded segments
below it — click one to play it back through a plain `<video>` element.
Closing the dialog clears the `<img>`'s `src`, releasing that browser
tab's relay subscription immediately rather than leaving a live
connection open to a dialog nobody's looking at.

A per-camera online/offline dot (`Relay.Connected`, set the moment its
upstream multipart reader is established and cleared the instant that
connection drops) shows next to the camera picker and above the live
view — a dead or unreachable camera used to just leave the live view
silently frozen with no explanation. The dialog re-polls
`GET /api/cameras/list` every 5s while it's open, so a camera going
down or coming back up shows up without needing to close and reopen it;
switching to (or discovering) an offline camera also clears the `<img>`'s
`src` rather than leaving whichever camera was viewed previously frozen
on screen looking like it's still live — an `<img>` only updates its
rendered bitmap on a successful load, so a pending request to a camera
that's never going to send a frame would otherwise just leave the old
one showing indefinitely.

## API

- `GET /api/cameras/list` → `{"cameras": [{"name", "label_ru", "label_en", "connected"}, ...]}`
  — empty if no cameras are configured.
- `GET /api/cameras/{name}/stream` → live MJPG, multipart/x-mixed-replace,
  as many simultaneous clients as want it.
- `GET /api/cameras/{name}/archive` → `{"segments": [{"name", "size_bytes", "last_modified"}, ...]}`,
  newest first; empty (not an error) if recording isn't enabled or
  nothing's been recorded yet.
- `GET /api/cameras/{name}/archive/{file}` → the raw segment file
  (`http.ServeContent`, so `Range` requests work — needed for a browser
  to seek inside the `<video>` element). `{file}` is rejected unless
  it's a plain filename (no path separators, no `..`), so this can never
  resolve outside that camera's own segment directory.
