# Dashcam (not a Bosun feature)

`docker-compose.yml`'s `dashcam` service is a cyclic (ring-buffer)
recorder for a WiFi camera's MJPG stream — riding on the same host/
compose file for convenience, but unrelated to Bosun's own code. No Go
changes, no `config.yaml` entry, nothing here talks to the LLM or the
tool registry.

## Why it can't end up in the S3 backup

`internal/backup.BuildArchive` only ever walks `cfg.Backup.DataDir`
(`./data/bosun`) — see `internal/backup/archive.go`. `dashcam` writes to
`./data/dashcam`, a sibling directory outside that tree, so it's excluded
by construction, not by convention or a manual exclude-list that could
later be forgotten.

## How it works

```
ffmpeg -reconnect 1 -reconnect_streamed 1 -reconnect_delay_max 2
  -i $DASHCAM_STREAM_URL
  -an -c:v libx264 -preset veryfast -crf 23 -pix_fmt yuv420p
  -f segment -segment_time $DASHCAM_SEGMENT_SECONDS
  -segment_wrap $DASHCAM_SEGMENT_COUNT -reset_timestamps 1
  /data/dashcam/cam_%03d.mp4
```

`-segment_wrap` is what makes this genuinely cyclic: ffmpeg itself
overwrites the oldest of `DASHCAM_SEGMENT_COUNT` numbered files once it
wraps around — no separate cleanup process, cron job, or retention script
needed. `-reconnect*` flags matter for a real WiFi camera: the stream
drops sometimes, and ffmpeg reconnects on its own rather than exiting;
`restart: unless-stopped` is the backstop if it exits anyway.

All camera-specific values live in `.env` (see `.env.example`) — nothing
camera-specific is hardcoded in `docker-compose.yml`.

## Storage math (measured live, not estimated)

Tested directly against a real 1024x768 MJPG stream (an ESP32-CAM-style
camera, custom firmware, no audio): both a pure remux (`-c:v copy`) and
the full libx264 encode ran at the *same* ~0.32x-realtime speed — the
bottleneck is the camera's own frame delivery (~8 fps actually delivered
despite an advertised 25 in the stream header), not this host's CPU. So
the libx264 encode (much smaller files) costs nothing extra here.

- Raw MJPG (no transcode): ~2.2 GB/hour
- libx264 crf23 (what's actually configured): ~1 GB/hour

The defaults (`DASHCAM_SEGMENT_SECONDS=300`, `DASHCAM_SEGMENT_COUNT=50`)
give a ~4.2-hour ring on ~4 GB total — an explicit MVP choice, not a
technical ceiling. To hold longer, raise `DASHCAM_SEGMENT_COUNT` (each
segment is roughly `DASHCAM_SEGMENT_SECONDS / 60 * 17 MB` at these
settings) or lower the camera's own resolution/quality to shrink the
per-hour size instead.

## Camera flakiness is expected, not a bug

A cheap WiFi camera dropping off the network is normal — `docker compose
ps` showing `dashcam` cycling through `Restarting` just means it's
waiting for the camera to come back; recording resumes automatically the
next time `-i $DASHCAM_STREAM_URL` connects. No manual intervention
needed unless it's been down long enough that you suspect the camera
itself needs a power cycle.
