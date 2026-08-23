package cameras

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

const upstreamBoundary = "fakecam"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// fakeCameraServer stands in for a real WiFi camera: writes frames as a
// properly terminated multipart/x-mixed-replace body (closing delimiter
// included — a real camera's stream never actually ends, but
// mime/multipart.Reader can't confirm where the *last* part's data ends
// without either a closing delimiter or more data arriving afterward, so
// without one the last frame in each batch would spuriously fail to
// parse with "unexpected EOF"), then lets the connection end (simulating
// a dropped stream, same as a real camera losing WiFi). If ready is
// non-nil, it waits for that channel to close before writing any frames
// — used to deterministically let a test finish subscribing to the
// relay before any frame is sent, since Relay only republishes to
// whoever's already subscribed when a frame arrives (by design: a live
// feed shows the latest frame, not a replay of everything since the
// upstream connection opened).
func fakeCameraServer(t *testing.T, frames [][]byte, ready <-chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+upstreamBoundary)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		if ready != nil {
			<-ready
		}
		for i, frame := range frames {
			if i > 0 {
				fmt.Fprint(w, "\r\n")
				// A real camera has a real frame interval (this
				// project's own ESP32-CAM measured at ~8fps live) —
				// without any pacing here, frames would publish faster
				// than Go can schedule a reader to drain a size-1
				// channel between them, and Relay's own by-design
				// "evict the stale frame, keep only the latest" backlog
				// policy (see publish) would then legitimately (and
				// misleadingly, for this test's purposes) drop some.
				time.Sleep(5 * time.Millisecond)
			}
			fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", upstreamBoundary, len(frame))
			w.Write(frame)
			flusher.Flush()
		}
		fmt.Fprintf(w, "\r\n--%s--\r\n", upstreamBoundary)
		flusher.Flush()
		// Connection ends here — simulates the camera dropping the
		// stream once there's genuinely nothing more to send.
	}))
	t.Cleanup(server.Close)
	return server
}

// readFrames connects to a Relay's own ServeHTTP endpoint (via a real
// httptest.Server wrapping it) as a real HTTP client and parses n frames
// back out, mirroring exactly what a browser or ffmpeg would receive.
// Signals connected (a send, not a close — callers may run several of
// these concurrently against one shared, appropriately buffered channel)
// once the response has started, so a test can wait for every subscriber
// to be registered before letting an upstream begin sending frames.
func readFrames(t *testing.T, relayServerURL string, n int, connected chan<- struct{}) [][]byte {
	t.Helper()
	resp, err := http.Get(relayServerURL)
	if err != nil {
		t.Fatalf("connect to relay: %v", err)
	}
	defer resp.Body.Close()
	if connected != nil {
		connected <- struct{}{}
	}
	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse relay content-type: %v", err)
	}
	reader := multipart.NewReader(resp.Body, params["boundary"])
	var frames [][]byte
	for i := 0; i < n; i++ {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("read part %d: %v", i, err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		frames = append(frames, data)
	}
	return frames
}

func TestRelayRelaysFramesToASubscriber(t *testing.T) {
	ready := make(chan struct{})
	upstream := fakeCameraServer(t, [][]byte{[]byte("frame-1"), []byte("frame-2"), []byte("frame-3")}, ready)
	relay := NewRelay("test", upstream.URL, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	relayServer := httptest.NewServer(http.HandlerFunc(relay.ServeHTTP))
	defer relayServer.Close()

	connected := make(chan struct{})
	framesCh := make(chan [][]byte, 1)
	go func() { framesCh <- readFrames(t, relayServer.URL, 3, connected) }()
	<-connected
	close(ready) // subscriber is registered — safe for upstream to start sending now

	frames := <-framesCh
	want := [][]byte{[]byte("frame-1"), []byte("frame-2"), []byte("frame-3")}
	for i, f := range frames {
		if !bytes.Equal(f, want[i]) {
			t.Errorf("frame %d = %q, want %q", i, f, want[i])
		}
	}
}

func TestRelayFansOutToMultipleSubscribers(t *testing.T) {
	frames := make([][]byte, 20)
	for i := range frames {
		frames[i] = []byte(fmt.Sprintf("frame-%d", i))
	}
	ready := make(chan struct{})
	upstream := fakeCameraServer(t, frames, ready)
	relay := NewRelay("test", upstream.URL, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	relayServer := httptest.NewServer(http.HandlerFunc(relay.ServeHTTP))
	defer relayServer.Close()

	const subscribers = 2
	connected := make(chan struct{}, subscribers)
	done := make(chan [][]byte, subscribers)
	for i := 0; i < subscribers; i++ {
		go func() { done <- readFrames(t, relayServer.URL, 5, connected) }()
	}
	for i := 0; i < subscribers; i++ {
		<-connected
	}
	close(ready) // both subscribers registered — safe for upstream to start sending now

	for i := 0; i < subscribers; i++ {
		got := <-done
		if len(got) != 5 {
			t.Errorf("subscriber got %d frames, want 5", len(got))
		}
	}
}

func TestRelaySlowSubscriberDoesNotBlockPublish(t *testing.T) {
	relay := NewRelay("test", "http://unused.invalid", discardLogger())
	fast := relay.subscribe()
	slow := relay.subscribe() // never drained

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			relay.publish([]byte(fmt.Sprintf("frame-%d", i)))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked — a slow/undrained subscriber must not stall the broadcast")
	}

	select {
	case f := <-fast:
		if string(f) != "frame-99" {
			t.Errorf("fast subscriber's last frame = %q, want frame-99", f)
		}
	default:
		t.Error("fast subscriber received nothing")
	}
	if len(slow) != 1 {
		t.Errorf("slow subscriber's channel len = %d, want exactly 1 (latest frame only, not queued)", len(slow))
	}
}

func TestRelayReconnectsAfterUpstreamDrop(t *testing.T) {
	upstream := fakeCameraServer(t, [][]byte{[]byte("only-frame")}, nil)
	relay := NewRelay("test", upstream.URL, discardLogger())
	relay.ReconnectDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	relayServer := httptest.NewServer(http.HandlerFunc(relay.ServeHTTP))
	defer relayServer.Close()

	// The fake server always serves the same one frame per connection and
	// then drops — reading it twice in a row through the relay only
	// works if the relay actually reconnected upstream after the first
	// drop, not just delivered one frame and stalled. No synchronization
	// needed here: unlike the ordered-frame tests above, any arriving
	// frame satisfies the assertion, so a lost frame during the race to
	// subscribe just means the next reconnect (every 20ms) delivers one.
	first := readFrames(t, relayServer.URL, 1, nil)
	second := readFrames(t, relayServer.URL, 1, nil)
	if string(first[0]) != "only-frame" || string(second[0]) != "only-frame" {
		t.Errorf("frames = %q, %q, want both to be the upstream's frame (proves a real reconnect happened)", first[0], second[0])
	}
}
