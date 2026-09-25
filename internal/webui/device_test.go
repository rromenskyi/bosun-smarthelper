package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/roman220/bosun-smarthelper/internal/agent"
	"github.com/roman220/bosun-smarthelper/internal/llm"
	"github.com/roman220/bosun-smarthelper/internal/voice"
)

func newDeviceTestServer(t *testing.T, asker *fakeAsker, stt *fakeSTTEngine, tts *fakeTTSEngine, token string) *httptest.Server {
	t.Helper()
	server := NewServer(asker, nil, 5*time.Second, "ru", nil)
	server.SetSTTEngine(stt)
	server.SetTTSEngine(tts)
	server.SetDeviceOptions(true, token, 10*time.Second, "")
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

func TestSentenceSplitter(t *testing.T) {
	var got []string
	s := sentenceSplitter{emit: func(c string) { got = append(got, c) }}
	for _, delta := range []string{"Да. ", "Сегодня ясно, ветер слабый, около ", "трёх метров в секунду. Волна ", "полметра! Хорошего ", "хода."} {
		s.feed(delta)
	}
	s.flush()
	want := []string{
		"Да. Сегодня ясно, ветер слабый, около трёх метров в секунду.",
		"Волна полметра! Хорошего хода.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chunks:\n got %q\nwant %q", got, want)
	}
}

func TestSentenceSplitterBreaksRunOnText(t *testing.T) {
	var got []string
	s := sentenceSplitter{emit: func(c string) { got = append(got, c) }}
	s.feed(strings.Repeat("слово ", 80)) // 480 bytes, no sentence end
	if len(got) == 0 || len(got[0]) > maxSpokenChunk {
		t.Fatalf("run-on text not broken up: %d chunks, first %d bytes", len(got), len(got))
	}
}

// streamingAsker emits its answer as deltas, pausing between them, so the test
// can see that speech starts before the answer is complete.
type streamingAsker struct {
	fakeAsker
	deltas []string
	pause  time.Duration
}

func (f *streamingAsker) AskWithHistoryStreaming(ctx context.Context, message string, _ []agent.HistoryMessage, _ string, onEvent func(agent.StepEvent)) (string, agent.TurnStats, error) {
	f.seen = message
	onEvent(agent.StepEvent{Type: "delta", Delta: llm.StreamDelta{Kind: "fold", Text: "tool call details"}})
	for _, d := range f.deltas {
		onEvent(agent.StepEvent{Type: "delta", Delta: llm.StreamDelta{Kind: "prose", Text: d}})
		time.Sleep(f.pause)
	}
	return strings.Join(f.deltas, ""), agent.TurnStats{}, nil
}

type recordingTTS struct {
	mu    sync.Mutex
	texts []string
	times []time.Time
}

func (r *recordingTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	r.times = append(r.times, time.Now())
	return pcm16ToWAV(make([]byte, deviceFrameBytes), deviceSampleRate), nil
}

func TestDeviceSpeaksSentencesWhileStreaming(t *testing.T) {
	asker := &streamingAsker{
		deltas: []string{"Первое предложение ответа достаточно длинное. ", "Второе тоже **важное** и довольно длинное. ", "Третье."},
		pause:  150 * time.Millisecond,
	}
	tts := &recordingTTS{}
	server := NewServer(asker, nil, 5*time.Second, "ru", nil)
	server.SetSTTEngine(&fakeSTTEngine{transcript: voice.Transcript{Text: "вопрос", Language: "ru"}})
	server.SetTTSEngine(tts)
	server.SetDeviceOptions(true, "", 10*time.Second, "")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	conn, _, err := dialDevice(t, ts, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	sendJSON(t, conn, map[string]any{"type": "hello", "device": "d1"})
	readUntil(t, conn, "hello", "")
	start := time.Now()
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "start"})
	conn.Write(context.Background(), websocket.MessageBinary, make([]byte, deviceSampleRate)) // 0.5 s
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "stop"})
	_, audio := readUntil(t, conn, "speak", "stop")

	tts.mu.Lock()
	defer tts.mu.Unlock()
	// Each finished sentence goes to TTS as soon as it's complete; the short
	// tail is spoken on its own at the end (earlier chunks are already playing).
	want := []string{"Первое предложение ответа достаточно длинное.", "Второе тоже важное и довольно длинное.", "Третье."}
	if strings.Join(tts.texts, "|") != strings.Join(want, "|") {
		t.Fatalf("synthesized %q, want %q", tts.texts, want)
	}
	// The first sentence must be synthesized before the asker finished (3 × 150 ms).
	if first := tts.times[0].Sub(start); first > 300*time.Millisecond {
		t.Errorf("first sentence synthesized %v after listen stop; not streaming", first)
	}
	if len(audio) != 3*deviceFrameBytes {
		t.Errorf("reply audio %d bytes, want %d", len(audio), 3*deviceFrameBytes)
	}
}

// slowAsker answers after a delay, so the thinking heartbeat must fire meanwhile.
type slowAsker struct {
	answer string
	delay  time.Duration
}

func (f *slowAsker) Ask(ctx context.Context, message string) (string, agent.TurnStats, error) {
	time.Sleep(f.delay)
	return f.answer, agent.TurnStats{}, nil
}

func TestDeviceThinkingHeartbeat(t *testing.T) {
	server := NewServer(&slowAsker{answer: "Готово, ответ достаточно длинный для одного фрагмента.", delay: 400 * time.Millisecond},
		nil, 5*time.Second, "ru", nil)
	server.SetSTTEngine(&fakeSTTEngine{transcript: voice.Transcript{Text: "вопрос", Language: "ru"}})
	server.SetTTSEngine(&fakeTTSEngine{audio: pcm16ToWAV(make([]byte, deviceFrameBytes), deviceSampleRate)})
	server.SetDeviceOptions(true, "", 10*time.Second, "")
	server.deviceThinkingInterval = 50 * time.Millisecond
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	conn, _, err := dialDevice(t, ts, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	sendJSON(t, conn, map[string]any{"type": "hello", "device": "d1"})
	readUntil(t, conn, "hello", "")
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "start"})
	conn.Write(context.Background(), websocket.MessageBinary, make([]byte, deviceSampleRate))
	sendJSON(t, conn, map[string]any{"type": "listen", "state": "stop"})
	texts, _ := readUntil(t, conn, "speak", "stop")

	thinking, afterStart := 0, false
	for _, m := range texts {
		switch {
		case m.Type == "speak" && m.State == "start":
			afterStart = true
		case m.Type == "thinking" && afterStart:
			t.Fatal("thinking heartbeat continued after the reply started")
		case m.Type == "thinking":
			thinking++
		}
	}
	// 400 ms of agent time at a 50 ms interval: the initial message plus several beats.
	if thinking < 4 {
		t.Fatalf("got %d thinking messages before the reply, want >= 4", thinking)
	}
}

// ctxAsker blocks until its context is cancelled for the first question and
// answers the second at once — a superseded turn.
type ctxAsker struct {
	mu    sync.Mutex
	calls int
}

func (f *ctxAsker) Ask(ctx context.Context, message string) (string, agent.TurnStats, error) {
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first {
		<-ctx.Done()
		return "", agent.TurnStats{}, ctx.Err()
	}
	return "Второй ответ достаточно длинный, чтобы быть одним фрагментом.", agent.TurnStats{}, nil
}

// sequentialSTT returns a different transcript per call.
type sequentialSTT struct {
	mu    sync.Mutex
	texts []string
}

func (f *sequentialSTT) Transcribe(_ context.Context, _ []byte) (voice.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.texts[0]
	f.texts = f.texts[1:]
	return voice.Transcript{Text: t, Language: "ru"}, nil
}

func TestDeviceSupersededQuestionLeavesNoHistory(t *testing.T) {
	server := NewServer(&ctxAsker{}, nil, 5*time.Second, "ru", nil)
	server.SetSTTEngine(&sequentialSTT{texts: []string{"первый вопрос", "второй вопрос"}})
	server.SetTTSEngine(&fakeTTSEngine{audio: pcm16ToWAV(make([]byte, deviceFrameBytes), deviceSampleRate)})
	server.SetDeviceOptions(true, "", 10*time.Second, "")
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	conn, _, err := dialDevice(t, ts, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	sendJSON(t, conn, map[string]any{"type": "hello", "device": "d1"})
	readUntil(t, conn, "hello", "")
	utter := func() {
		sendJSON(t, conn, map[string]any{"type": "listen", "state": "start"})
		conn.Write(context.Background(), websocket.MessageBinary, make([]byte, deviceSampleRate))
		sendJSON(t, conn, map[string]any{"type": "listen", "state": "stop"})
	}
	utter()
	readUntil(t, conn, "thinking", "") // the first turn is running (blocked in the agent)
	time.Sleep(50 * time.Millisecond)
	utter() // supersedes it
	readUntil(t, conn, "speak", "stop")

	history := server.loadHistory(deviceSessionID("d1"))
	var got []string
	for _, m := range history {
		got = append(got, m.Role+":"+m.Content)
	}
	want := []string{"user:второй вопрос", "assistant:Второй ответ достаточно длинный, чтобы быть одним фрагментом."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history:\n got %q\nwant %q", got, want)
	}
}

func TestDropUnansweredUserMessageOnlyRemovesThatMessage(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.saveUserMessage("sess-0001", "вопрос", false)
	server.saveAssistantReply("sess-0001", "ответ", agent.TurnStats{}, 1)
	server.dropUnansweredUserMessage("sess-0001", "вопрос") // last entry is the reply: no-op
	if n := len(server.loadHistory("sess-0001")); n != 2 {
		t.Fatalf("answered question removed: %d entries left", n)
	}
	server.saveUserMessage("sess-0001", "ещё", false)
	server.dropUnansweredUserMessage("sess-0001", "другое") // different text: no-op
	server.dropUnansweredUserMessage("sess-0001", "ещё")
	if n := len(server.loadHistory("sess-0001")); n != 2 {
		t.Fatalf("got %d entries, want 2", n)
	}
}
