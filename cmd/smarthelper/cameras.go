package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

// resolveCameraDataDir is a sibling of resolveDataDir's own default
// (~/.local/share/bosun), not inside it — camera archives
// (internal/cameras, docs/cameras.md) must stay outside
// cfg.Backup.DataDir so they're excluded from the S3 backup by
// construction, the same reasoning docs/dashcam.md documented for the
// standalone service this replaces.
func resolveCameraDataDir() (string, error) {
	bosunDataDir, err := resolveDataDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(bosunDataDir), "dashcam"), nil
}

// internalRecorderBaseURL is the address a camera recorder's ffmpeg
// subprocess uses to reach its own camera's relay endpoint
// (/api/cameras/<name>/stream) — never the camera directly, so the
// camera's single client slot stays reserved for the relay itself (see
// docs/cameras.md). Always plain HTTP, even when the web UI serves TLS
// externally: this is a loopback-only call, and giving ffmpeg a
// self-signed/Let's-Encrypt cert to validate would be needless
// complexity for a connection that never leaves the host. Returns "" if
// TLS is configured with no plain-HTTP fallback bind at all, meaning
// there's genuinely no way to reach the relay without a cert — recording
// simply can't work with that configuration.
func internalRecorderBaseURL(cfg *config.Config) string {
	addr := cfg.Web.Bind
	if cfg.Web.TLSCertFile != "" && cfg.Web.TLSKeyFile != "" {
		if cfg.Web.HTTPFallbackBind == "" {
			return ""
		}
		addr = cfg.Web.HTTPFallbackBind
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return "http://127.0.0.1:" + port
}

// runCameraRecorder cyclically records one camera's relay stream (never
// the camera directly — see internalRecorderBaseURL) via the same
// ffmpeg segment-wrap invocation proven live in the standalone `dashcam`
// service this replaces (docs/cameras.md). If ffmpeg exits for any
// reason (the relay restarting, a transient hiccup), this waits a few
// seconds and starts it again, until ctx is cancelled.
func runCameraRecorder(ctx context.Context, cam config.CameraConfig, baseURL, dataDir string, logger *slog.Logger) {
	segmentSeconds := cam.SegmentSeconds
	if segmentSeconds <= 0 {
		segmentSeconds = 300
	}
	segmentCount := cam.SegmentCount
	if segmentCount <= 0 {
		segmentCount = 50
	}
	camDir := filepath.Join(dataDir, cam.Name)
	if err := os.MkdirAll(camDir, 0o755); err != nil {
		logger.Error("create camera archive directory", "camera", cam.Name, "error", err)
		return
	}

	args := []string{
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "2",
		"-i", fmt.Sprintf("%s/api/cameras/%s/stream", baseURL, cam.Name),
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-f", "segment", "-segment_time", strconv.Itoa(segmentSeconds),
		"-segment_wrap", strconv.Itoa(segmentCount), "-reset_timestamps", "1",
		filepath.Join(camDir, "cam_%03d.mp4"),
	}

	const restartDelay = 5 * time.Second
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil && ctx.Err() == nil {
			logger.Warn("camera recorder exited, restarting", "camera", cam.Name, "error", err, "stderr", stderr.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(restartDelay):
		}
	}
}
