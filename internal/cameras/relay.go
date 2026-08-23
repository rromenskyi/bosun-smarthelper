// Package cameras relays a WiFi camera's MJPG stream to any number of
// consumers — the one thing that makes this necessary at all is that
// these cheap cameras only accept a single client. Without a relay, an
// ffmpeg recorder and a live browser viewer would fight over that one
// slot; with it, the relay holds the camera's one connection and fans
// each frame out to as many subscribers as actually want it (a
// recording process, any number of browser tabs) — see docs/cameras.md.
package cameras

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

// localBoundary separates frames on Relay's own outgoing connections —
// independent of whatever boundary string the upstream camera happens to
// use, since each hop is its own multipart stream.
const localBoundary = "bosuncamera"

// Relay owns exactly one upstream HTTP connection to a camera's MJPG
// stream (StreamURL) and republishes every frame it reads to current
// subscribers.
type Relay struct {
	Name      string
	StreamURL string
	Logger    *slog.Logger
	// ReconnectDelay is how long to wait after a failed/dropped upstream
	// connection before trying again — a real WiFi camera drops
	// occasionally, same as any wireless device, and this shouldn't need
	// a process restart to recover. Defaults to 2s (see NewRelay); tests
	// override it directly to keep from waiting on the real value.
	ReconnectDelay time.Duration

	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
}

// NewRelay builds a Relay for one camera. Run must be called (typically
// in its own goroutine) for it to actually connect and start publishing.
func NewRelay(name, streamURL string, logger *slog.Logger) *Relay {
	return &Relay{
		Name:           name,
		StreamURL:      streamURL,
		Logger:         logger,
		ReconnectDelay: 2 * time.Second,
		subscribers:    map[chan []byte]struct{}{},
	}
}

// Run connects to StreamURL and republishes every frame to current
// subscribers until ctx is cancelled, reconnecting with ReconnectDelay
// backoff on any error.
func (r *Relay) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := r.connectOnce(ctx); err != nil && ctx.Err() == nil {
			r.Logger.Warn("camera relay disconnected", "camera", r.Name, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.ReconnectDelay):
		}
	}
}

func (r *Relay) connectOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.StreamURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("camera returned HTTP %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return fmt.Errorf("unexpected content-type %q, want multipart/x-mixed-replace", contentType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return fmt.Errorf("multipart response missing a boundary")
	}

	reader := multipart.NewReader(resp.Body, boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return fmt.Errorf("read part: %w", err)
		}
		frame, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		if len(frame) > 0 {
			r.publish(frame)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// publish sends frame to every current subscriber without blocking on a
// slow one: a full channel has its one buffered (now-stale) frame
// evicted and replaced, so a slow client always sees the latest frame
// next rather than queuing behind ones it'll never catch up to, and one
// bad connection can never stall the upstream reader or other
// subscribers.
func (r *Relay) publish(frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subscribers {
		select {
		case ch <- frame:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- frame:
			default:
			}
		}
	}
}

func (r *Relay) subscribe() chan []byte {
	ch := make(chan []byte, 1)
	r.mu.Lock()
	r.subscribers[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

func (r *Relay) unsubscribe(ch chan []byte) {
	r.mu.Lock()
	delete(r.subscribers, ch)
	r.mu.Unlock()
}

// ServeHTTP registers the request as a new subscriber and streams frames
// to it as multipart/x-mixed-replace until the client disconnects — a
// browser <img src=...> renders this natively (no player library), and
// ffmpeg's own mjpeg demuxer understands it too (this is exactly the
// format internal/cameras itself parses from the upstream camera).
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Subscribed before any byte reaches the client — otherwise a caller
	// that starts reading as soon as it sees a response (even just
	// headers) could race the subscription itself and miss the very
	// first frame(s) published in that window.
	ch := r.subscribe()
	defer r.unsubscribe(ch)

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+localBoundary)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-req.Context().Done():
			return
		case frame := <-ch:
			if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", localBoundary, len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
