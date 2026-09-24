package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/roman220/bosun-smarthelper/internal/voice"
)

func newDeviceTestServer(t *testing.T, asker *fakeAsker, stt *fakeSTTEngine, tts *fakeTTSEngine, token string) *httptest.Server {
	t.Helper()
	server := NewServer(asker, nil, 5*time.Second, "ru", nil)
	server.SetSTTEngine(stt)
	server.SetTTSEngine(tts)
	server.SetDeviceOptions(true, token, 10*time.Second)
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func dialDevice(t *testing.T, ts *httptest.Server, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{}
	if token != "" {
		opts.HTTPHeader = http.Header{"Authorization": {"Bearer " + token}}
	}
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/device", opts)
}

func sendJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readUntil collects messages until a text message of the given type/state.
func readUntil(t *testing.T, conn *websocket.Conn, typ, state string) (texts []deviceMessage, audio []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read (waiting for %s %s): %v", typ, state, err)
		}
		if kind == websocket.MessageBinary {
			audio = append(audio, data...)
			continue
		}
		var m deviceMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("bad json %q: %v", data, err)
		}
		texts = append(texts, m)
		if m.Type == typ && m.State == state {
			return texts, audio
		}
	}
}

func TestDeviceFullTurn(t *testing.T) {
	asker := &fakeAsker{answer: "**Всё** в порядке, капитан."}
	stt := &fakeSTTEngine{transcript: voice.Transcript{Text: "как дела", Language: "ru"}}
	reply := make([]byte, deviceFrameBytes*3+100) // 3 full frames + a partial one
	for i := range reply {
		reply[i] = byte(i)
	}
	tts := &fakeTTSEngine{audio: pcm16ToWAV(reply, deviceSampleRate)}
	ts := newDeviceTestServer(t, asker, stt, tts, "")

	conn, _, err := dialDevice(t, ts, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	sendJSON(t, conn, map[string]any{"type": "hello", "protocol": 1, "device": "esp32-speaker-ABC123", "firmware": "test"})
	hello, _ := readUntil(t, conn, "hello", "")
	if got := hello[len(hello)-1].Session; got != "device-esp32-speaker-abc123" {
		t.Fatalf("session = %q", got)
	}

	sendJSON(t, conn, map[string]any{"type": "listen", "state": "start", "reason": "button"})
	utterance := make([]byte, deviceSampleRate*2) // 1 s of silence
	for i := 0; i < len(utterance); i += deviceFrameBytes {
		if err := conn.Write(context.Background(), websocket.MessageBinary, utterance[i:i+deviceFrameBytes]); err != nil {
			t.Fatalf("write audio: %v", err)
		}
	}
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "stop", "reason": "button"})

	texts, audio := readUntil(t, conn, "speak", "stop")
	if texts[0].Type != "thinking" {
		t.Errorf("first reply message = %+v, want thinking", texts[0])
	}
	if string(audio) != string(reply) {
		t.Errorf("reply audio: got %d bytes, want %d identical bytes", len(audio), len(reply))
	}
	if asker.seen != "как дела" {
		t.Errorf("agent saw %q", asker.seen)
	}
	if tts.text != "Всё в порядке, капитан." {
		t.Errorf("tts got %q (markdown not stripped?)", tts.text)
	}
	if len(stt.gotWAV) != 44+len(utterance) || string(stt.gotWAV[:4]) != "RIFF" {
		t.Errorf("stt got %d bytes, want a %d-byte WAV", len(stt.gotWAV), 44+len(utterance))
	}
}

func TestDeviceRejectsBadToken(t *testing.T) {
	ts := newDeviceTestServer(t, &fakeAsker{}, &fakeSTTEngine{}, &fakeTTSEngine{}, "secret")
	if _, resp, err := dialDevice(t, ts, "wrong"); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got err=%v resp=%v", err, resp)
	}
	conn, _, err := dialDevice(t, ts, "secret")
	if err != nil {
		t.Fatalf("dial with the right token: %v", err)
	}
	conn.CloseNow()
}

func TestDeviceDisabledIsNotFound(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/device", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeviceShortUtteranceIsAnError(t *testing.T) {
	asker := &fakeAsker{answer: "x"}
	ts := newDeviceTestServer(t, asker, &fakeSTTEngine{}, &fakeTTSEngine{}, "")
	conn, _, err := dialDevice(t, ts, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	sendJSON(t, conn, map[string]any{"type": "hello", "device": "d1"})
	readUntil(t, conn, "hello", "")
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "start"})
	conn.Write(context.Background(), websocket.MessageBinary, make([]byte, deviceFrameBytes))
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "stop"})
	texts, _ := readUntil(t, conn, "error", "")
	if texts[len(texts)-1].Message != "utterance too short" || asker.seen != "" {
		t.Fatalf("got %+v, agent saw %q", texts, asker.seen)
	}
}

func TestPlainPCM16(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	got, ok := plainPCM16(pcm16ToWAV(pcm, 16000), 16000)
	if !ok || string(got) != string(pcm) {
		t.Fatalf("round trip: ok=%v got=%v", ok, got)
	}
	if _, ok := plainPCM16(pcm16ToWAV(pcm, 22050), 16000); ok {
		t.Fatal("a 22.05 kHz WAV must not pass as 16 kHz")
	}
}

func TestStripMarkdownForSpeech(t *testing.T) {
	in := "# Title\n**bold** and [a link](https://example.com) ![img](x.png) $x$"
	if got, want := stripMarkdownForSpeech(in), "Title bold and a link x"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDeviceSessionID(t *testing.T) {
	for in, want := range map[string]string{
		"esp32-speaker-b2add8": "device-esp32-speaker-b2add8",
		"Кухня <1>":            "device-1",
		"":                     "device-unnamed",
	} {
		if got := deviceSessionID(in); got != want || !validSessionID(got) {
			t.Errorf("deviceSessionID(%q) = %q (valid=%v), want %q", in, got, validSessionID(got), want)
		}
	}
}
