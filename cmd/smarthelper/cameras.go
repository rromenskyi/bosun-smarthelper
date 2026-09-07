package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/alerts"
	"github.com/roman220/bosun-smarthelper/internal/cameras"
	"github.com/roman220/bosun-smarthelper/internal/config"
	"github.com/roman220/bosun-smarthelper/internal/errlog"
	"github.com/roman220/bosun-smarthelper/internal/filedump"
	"github.com/roman220/bosun-smarthelper/internal/notifications"
	"github.com/roman220/bosun-smarthelper/internal/persondetect"
	"github.com/roman220/bosun-smarthelper/internal/settings"
	"github.com/roman220/bosun-smarthelper/internal/voice"
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

// cameraSecurityDefaultInterval is used when settings.Data.
// CameraSecurityIntervalSeconds is unset (0) — a busy-loop hammering a
// CPU-only YOLOv8n inference on every camera isn't a reasonable
// "unconfigured" default the way it would be for, say, a metrics poll.
const cameraSecurityDefaultInterval = 30 * time.Second

// cameraSecurityTick is how often the checker wakes up to see whether
// any camera is actually due — independent of (and much shorter than)
// the per-camera interval itself, the same "tick vs. due" split
// runBackupScheduler uses.
const cameraSecurityTick = 5 * time.Second

// cameraSecurityNotifiers assembles the channels a person-detection
// event fires through: the notification zone always, plus a spoken
// announcement if a speaker channel is configured and its existing
// global toggle (the same one threshold/NOAA alerts already share) is
// on — camera security doesn't get its own separate speaker toggle,
// since "do I want alerts spoken aloud" is one preference, not one per
// alert source.
func cameraSecurityNotifiers(cfg *config.Config, settingsStore *settings.Store, ttsEngine voice.TTSEngine, logger *slog.Logger, notificationStore *notifications.Store) []alerts.Notifier {
	data := settingsStore.Get()
	return collectNotifiers(
		speakerNotifier(cfg.Alerts.Channels.Speaker, data.AlertsSpeakerEnabled, ttsEngine, data.DefaultLanguage, logger),
		notificationNotifier(notificationStore),
	)
}

// runCameraSecurityChecker polls every connected camera for a person
// (internal/persondetect — a small local YOLOv8n model, not the
// vision-capable LLM path: see docs/cameras.md for why continuous
// polling needs to be this fast/cheap instead) once
// settings.Data.CameraSecurityEnabled is on. Edge-triggered per camera
// (personPresent) — a person standing in frame for several checks in a
// row fires exactly one alert, not one per tick; the same reasoning
// already applied to the NOAA position-resolve failure notification.
func runCameraSecurityChecker(
	ctx context.Context,
	cameraManager *cameras.Manager,
	detectClient *persondetect.Client,
	fileDumpStore *filedump.Store,
	cfg *config.Config,
	settingsStore *settings.Store,
	ttsEngine voice.TTSEngine,
	logger *slog.Logger,
	errLog *errlog.Logger,
	notificationStore *notifications.Store,
) {
	ticker := time.NewTicker(cameraSecurityTick)
	defer ticker.Stop()

	lastChecked := map[string]time.Time{}
	personPresent := map[string]bool{}
	checkFailing := map[string]bool{}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data := settingsStore.Get()
			if !data.CameraSecurityEnabled {
				continue
			}
			interval := time.Duration(data.CameraSecurityIntervalSeconds) * time.Second
			if interval <= 0 {
				interval = cameraSecurityDefaultInterval
			}
			for _, cam := range cameraManager.List() {
				if time.Since(lastChecked[cam.Name]) < interval {
					continue
				}
				relay, ok := cameraManager.Relay(cam.Name)
				if !ok || !relay.Connected() {
					continue
				}
				lastChecked[cam.Name] = time.Now()
				checkCameraForPerson(ctx, cam, relay, detectClient, fileDumpStore, cfg, settingsStore, ttsEngine, logger, errLog, notificationStore, personPresent, checkFailing)
			}
		}
	}
}

func checkCameraForPerson(
	ctx context.Context,
	cam cameras.Config,
	relay *cameras.Relay,
	detectClient *persondetect.Client,
	fileDumpStore *filedump.Store,
	cfg *config.Config,
	settingsStore *settings.Store,
	ttsEngine voice.TTSEngine,
	logger *slog.Logger,
	errLog *errlog.Logger,
	notificationStore *notifications.Store,
	personPresent map[string]bool,
	checkFailing map[string]bool,
) {
	language := settingsStore.Get().DefaultLanguage
	label := cam.LabelRU
	if language == "en" && cam.LabelEN != "" {
		label = cam.LabelEN
	}
	if label == "" {
		label = cam.Name
	}

	snapCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	frame, err := relay.Snapshot(snapCtx)
	cancel()
	if err != nil {
		logger.Warn("camera security snapshot", "camera", cam.Name, "error", err)
		errLog.Record("camera_security", "snapshot:"+cam.Name, err)
		if !checkFailing[cam.Name] {
			notificationStore.Add(notifications.Notification{
				Source: "camera_security", Severity: "warning",
				Title: fmt.Sprintf("Could not get a frame from %s", label), Body: err.Error(),
			})
			checkFailing[cam.Name] = true
		}
		return
	}

	detectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	result, err := detectClient.Detect(detectCtx, frame)
	cancel()
	if err != nil {
		logger.Warn("camera security detect", "camera", cam.Name, "error", err)
		errLog.Record("camera_security", "detect:"+cam.Name, err)
		if !checkFailing[cam.Name] {
			notificationStore.Add(notifications.Notification{
				Source: "camera_security", Severity: "warning",
				Title: fmt.Sprintf("Person detection failed for %s", label), Body: err.Error(),
			})
			checkFailing[cam.Name] = true
		}
		return
	}
	checkFailing[cam.Name] = false

	wasPresent := personPresent[cam.Name]
	personPresent[cam.Name] = result.PersonDetected
	if !result.PersonDetected || wasPresent {
		// Either nothing there, or already alerted for this same
		// continuous presence — nothing new to report.
		return
	}

	now := time.Now()
	title := fmt.Sprintf("Person detected: %s", label)
	if language == "ru" {
		title = fmt.Sprintf("Обнаружен человек: %s", label)
	}
	body := fmt.Sprintf("confidence %.0f%%, %d detected", result.Confidence*100, result.Count)
	if language == "ru" {
		body = fmt.Sprintf("уверенность %.0f%%, обнаружено: %d", result.Confidence*100, result.Count)
	}

	relPath := ""
	folder := path.Join("cameras", cam.Name)
	filename := now.UTC().Format("2006-01-02T15-04-05Z") + ".jpg"
	if fileDumpStore != nil {
		if dest, rel, err := fileDumpStore.OpenForWrite(folder, filename); err != nil {
			logger.Warn("save camera security frame", "camera", cam.Name, "error", err)
			errLog.Record("camera_security", "save_frame:"+cam.Name, err)
		} else if _, err := dest.Write(frame); err != nil {
			dest.Close()
			logger.Warn("save camera security frame", "camera", cam.Name, "error", err)
			errLog.Record("camera_security", "save_frame:"+cam.Name, err)
		} else if err := dest.Close(); err != nil {
			logger.Warn("save camera security frame", "camera", cam.Name, "error", err)
			errLog.Record("camera_security", "save_frame:"+cam.Name, err)
		} else {
			relPath = rel
		}
	}
	if relPath != "" {
		body += " — /files/" + relPath
	}

	alert := alerts.Alert{
		Source: "camera_security", Severity: alerts.SeverityWarning,
		Title: title, Body: body, At: now,
	}
	for _, notifier := range cameraSecurityNotifiers(cfg, settingsStore, ttsEngine, logger, notificationStore) {
		if err := notifier.Notify(ctx, alert); err != nil {
			logger.Warn("camera security notify", "camera", cam.Name, "error", err)
			errLog.Record("camera_security", "notify:"+cam.Name, err)
		}
	}
}
