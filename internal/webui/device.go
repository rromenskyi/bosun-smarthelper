package webui

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/roman220/bosun-smarthelper/internal/agent"
)

// Hardware voice devices (docs/devices.md). A device keeps one WebSocket open
// to GET /api/device and speaks the protocol defined by the ESP32 speaker
// firmware (github.com/rromenskyi/esp32-speaker, docs/PROTOCOL.md): JSON text
// frames for control, binary frames of 20 ms mono pcm16 at 16 kHz for audio.
// One utterance (listen start..stop) becomes one chat turn in a per-device
// session, answered through the same STT -> agent -> TTS path as the web UI.

const (
	deviceSampleRate   = 16000
	deviceFrameBytes   = deviceSampleRate * 2 * 20 / 1000 // 20 ms of pcm16 mono
	deviceMinUtterance = deviceSampleRate * 2 / 4         // < 250 ms: treat as a mis-press
	deviceProtocol     = 1
)

// SetDeviceOptions enables GET /api/device (see config.DevicesConfig).
// token empty means devices aren't authenticated.
func (s *Server) SetDeviceOptions(enabled bool, token string, maxUtterance time.Duration, responseHint string) {
	if maxUtterance <= 0 {
		maxUtterance = 30 * time.Second
	}
	s.deviceEnabled = enabled
	s.deviceToken = token
	s.deviceMaxUtterance = maxUtterance
	s.deviceResponseHint = responseHint
}

type deviceMessage struct {
	Type     string `json:"type"`
	State    string `json:"state,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Device   string `json:"device,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	Session  string `json:"session,omitempty"`
	Message  string `json:"message,omitempty"`
	Name     string `json:"name,omitempty"`
	Action   string `json:"action,omitempty"`
}

// deviceLink is one connected device.
type deviceLink struct {
	s       *Server
	conn    *websocket.Conn
	name    string
	session string

	mu        sync.Mutex
	listening bool
	audio     bytes.Buffer
	cancel    context.CancelFunc // in-flight turn, if any
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	if !s.deviceEnabled {
		http.NotFound(w, r)
		return
	}
	if s.deviceToken != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.deviceToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	if s.sttEngine == nil || s.ttsEngine == nil {
		http.Error(w, "voice is not configured", http.StatusServiceUnavailable)
		return
	}
	// The HTTP server's read/write timeouts are armed on the connection
	// before the handler runs and survive the hijack; a long-lived device
	// link would otherwise be cut after requestTimeout.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.logger.Warn("device websocket accept failed", "error", err)
		return
	}
	conn.SetReadLimit(1 << 20)
	link := &deviceLink{s: s, conn: conn}
	err = link.run(r.Context())
	link.stopTurn()
	status := websocket.CloseStatus(err)
	if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || errors.Is(err, context.Canceled) {
		s.logger.Info("device disconnected", "device", link.name)
	} else {
		s.logger.Warn("device link closed", "device", link.name, "error", err)
	}
	conn.CloseNow()
}

func (l *deviceLink) run(ctx context.Context) error {
	for {
		kind, data, err := l.conn.Read(ctx)
		if err != nil {
			return err
		}
		if kind == websocket.MessageBinary {
			l.mu.Lock()
			if l.listening && l.audio.Len() < l.maxBytes() {
				l.audio.Write(data)
			}
			l.mu.Unlock()
			continue
		}
		var m deviceMessage
		if json.Unmarshal(data, &m) != nil {
			continue // unknown/garbled control messages are ignored by design
		}
		l.handle(ctx, m)
	}
}

func (l *deviceLink) maxBytes() int {
	return int(l.s.deviceMaxUtterance.Seconds()) * deviceSampleRate * 2
}

func (l *deviceLink) handle(ctx context.Context, m deviceMessage) {
	switch {
	case m.Type == "hello":
		l.name = m.Device
		l.session = deviceSessionID(m.Device)
		l.s.logger.Info("device connected", "device", m.Device, "firmware", m.Firmware, "protocol", m.Protocol)
		l.send(ctx, deviceMessage{Type: "hello", Protocol: deviceProtocol, Session: l.session})
	case m.Type == "listen" && m.State == "start":
		l.stopTurn() // a new utterance supersedes an unfinished reply
		l.mu.Lock()
		l.listening = true
		l.audio.Reset()
		l.mu.Unlock()
	case m.Type == "listen" && m.State == "stop":
		l.mu.Lock()
		audio := append([]byte(nil), l.audio.Bytes()...)
		wasListening := l.listening
		l.listening = false
		l.audio.Reset()
		l.mu.Unlock()
		if !wasListening {
			return
		}
		if len(audio) < deviceMinUtterance {
			l.send(ctx, deviceMessage{Type: "error", Message: "utterance too short"})
			return
		}
		l.startTurn(ctx, audio)
	case m.Type == "abort":
		l.s.logger.Info("device aborted the reply", "device", l.name, "reason", m.Reason)
		l.stopTurn()
	case m.Type == "speak" && m.State == "done":
		// Informational: playback finished on the device.
	case m.Type == "button":
		l.s.logger.Debug("device button", "device", l.name, "button", m.Name, "action", m.Action)
	}
}

func (l *deviceLink) startTurn(parent context.Context, audio []byte) {
	ctx, cancel := context.WithTimeout(parent, l.s.requestTimeout)
	l.mu.Lock()
	l.cancel = cancel
	l.mu.Unlock()
	go func() {
		defer cancel()
		if err := l.turn(ctx, audio); err != nil && ctx.Err() == nil {
			l.s.logger.Error("device turn failed", "device", l.name, "error", err)
			l.send(parent, deviceMessage{Type: "error", Message: "request failed"})
		}
	}()
}

func (l *deviceLink) stopTurn() {
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.mu.Unlock()
}

// turn runs one utterance through STT -> agent -> TTS. The agent's answer is
// streamed and spoken sentence by sentence: the first sentence plays while the
// model is still writing the rest, instead of after the whole reply has been
// generated and synthesized.
func (l *deviceLink) turn(ctx context.Context, audio []byte) error {
	start := time.Now()
	l.send(ctx, deviceMessage{Type: "thinking"})

	transcript, err := l.s.sttEngine.Transcribe(ctx, pcm16ToWAV(audio, deviceSampleRate))
	if err != nil {
		return fmt.Errorf("transcribe: %w", err)
	}
	text := strings.TrimSpace(transcript.Text)
	sttDone := time.Now()
	if text == "" {
		l.send(ctx, deviceMessage{Type: "error", Message: "no speech recognized"})
		return nil
	}
	language := transcript.Language
	if language != "ru" && language != "en" {
		language = l.s.getDefaultLanguage()
	}

	sentences := make(chan string, 32)
	spoken := make(chan speakResult, 1)
	go func() { spoken <- l.speak(ctx, sentences) }()
	splitter := sentenceSplitter{emit: func(s string) { sentences <- s }}
	_, askErr := l.s.deviceAsk(agent.WithResponseHint(ctx, l.s.deviceResponseHint), l.session, text, language, splitter.feed)
	splitter.flush()
	close(sentences)
	result := <-spoken
	if askErr != nil {
		return fmt.Errorf("ask: %w", askErr)
	}
	if result.err != nil {
		return result.err
	}
	l.s.logger.Info("device turn",
		"device", l.name,
		"utterance_ms", len(audio)*1000/(deviceSampleRate*2),
		"text", text,
		"stt_ms", sttDone.Sub(start).Milliseconds(),
		"first_audio_ms", result.firstAudio.Sub(start).Milliseconds(),
		"total_ms", time.Since(start).Milliseconds(),
		"sentences", result.sentences,
		"reply_ms", result.pcmBytes*1000/(deviceSampleRate*2))
	return nil
}

type speakResult struct {
	firstAudio time.Time
	sentences  int
	pcmBytes   int
	err        error
}

// speak synthesizes each sentence as it arrives and streams it to the device,
// framing the whole reply in one speak start/stop.
func (l *deviceLink) speak(ctx context.Context, sentences <-chan string) (r speakResult) {
	defer func() {
		for range sentences { // unblock the producer after an early return
		}
	}()
	for sentence := range sentences {
		text := stripMarkdownForSpeech(sentence)
		if text == "" {
			continue
		}
		wav, err := l.s.ttsEngine.Synthesize(ctx, text)
		if err != nil {
			r.err = fmt.Errorf("synthesize: %w", err)
			return r
		}
		pcm, err := wavToDevicePCM(ctx, wav)
		if err != nil {
			r.err = fmt.Errorf("convert reply audio: %w", err)
			return r
		}
		if r.sentences == 0 {
			r.firstAudio = time.Now()
			if r.err = l.send(ctx, deviceMessage{Type: "speak", State: "start"}); r.err != nil {
				return r
			}
		}
		// Faster than real time; the device buffers and pushes back over TCP.
		for i := 0; i < len(pcm); i += deviceFrameBytes {
			end := min(i+deviceFrameBytes, len(pcm))
			if r.err = l.conn.Write(ctx, websocket.MessageBinary, pcm[i:end]); r.err != nil {
				return r
			}
		}
		r.sentences++
		r.pcmBytes += len(pcm)
	}
	if r.sentences == 0 {
		if ctx.Err() == nil {
			l.send(ctx, deviceMessage{Type: "error", Message: "empty answer"})
		}
		return r
	}
	r.err = l.send(ctx, deviceMessage{Type: "speak", State: "stop"})
	return r
}

// sentenceSplitter turns a stream of text deltas into speakable chunks: it
// emits at sentence ends once a chunk is long enough to be worth a TTS call
// (Piper starts a process per call), and flushes the remainder at the end.
type sentenceSplitter struct {
	buf  strings.Builder
	emit func(string)
}

const (
	minSpokenChunk = 40  // bytes (~20 Cyrillic letters): don't synthesize "Да." on its own
	maxSpokenChunk = 300 // break very long run-on text at a comma or space
)

func (s *sentenceSplitter) feed(delta string) {
	s.buf.WriteString(delta)
	for {
		text := s.buf.String()
		cut := sentenceCut(text)
		if cut < 0 {
			return
		}
		s.buf.Reset()
		s.buf.WriteString(text[cut:])
		if chunk := strings.TrimSpace(text[:cut]); chunk != "" {
			s.emit(chunk)
		}
	}
}

func (s *sentenceSplitter) flush() {
	if chunk := strings.TrimSpace(s.buf.String()); chunk != "" {
		s.emit(chunk)
	}
	s.buf.Reset()
}

// sentenceCut returns the index just past the first sentence end at or after
// minSpokenChunk bytes (a ".!?…" or newline followed by whitespace), a comma/
// space break past maxSpokenChunk, or -1 if the text should keep accumulating.
func sentenceCut(text string) int {
	for i, r := range text {
		if i < minSpokenChunk {
			continue
		}
		switch r {
		case '.', '!', '?', '…', '\n':
			next := i + len(string(r))
			if next < len(text) && (text[next] == ' ' || text[next] == '\n') {
				return next
			}
		}
	}
	if len(text) > maxSpokenChunk {
		if i := strings.LastIndexAny(text[:maxSpokenChunk], ",;: "); i > minSpokenChunk {
			return i + 1
		}
		return maxSpokenChunk
	}
	return -1
}

func (l *deviceLink) send(ctx context.Context, m deviceMessage) error {
	data, _ := json.Marshal(m)
	return l.conn.Write(ctx, websocket.MessageText, data)
}

// deviceAsk runs a transcribed utterance through the agent like handleChat:
// per-session history, local-model queueing, persisted turns. onProse gets the
// answer's prose as it streams (all at once if the asker can't stream).
func (s *Server) deviceAsk(ctx context.Context, sessionID, message, language string, onProse func(string)) (string, error) {
	if s.status().Provider == "local" {
		turn, _ := s.local.join()
		select {
		case <-turn:
			defer s.local.release()
			defer s.markChatActivity()
		case <-ctx.Done():
			s.local.abandon(turn)
			return "", ctx.Err()
		}
	}
	history := s.loadHistory(sessionID)
	s.saveUserMessage(sessionID, message, false)
	history = s.compactHistoryForTurn(ctx, sessionID, history)

	start := time.Now()
	var answer string
	var stats agent.TurnStats
	var err error
	streamed := false
	if streamer, ok := s.asker.(streamingConversationAsker); ok {
		streamed = true
		answer, stats, err = streamer.AskWithHistoryStreaming(ctx, message, history, language, func(e agent.StepEvent) {
			// Only the answer's prose is spoken; "fold" deltas are tool-call details.
			if e.Type == "delta" && e.Delta.Kind == "prose" {
				onProse(e.Delta.Text)
			}
		})
	} else if conversational, ok := s.asker.(conversationAsker); ok {
		answer, stats, err = conversational.AskWithHistory(ctx, message, history, language)
	} else {
		answer, stats, err = s.asker.Ask(ctx, message)
	}
	if err != nil {
		return "", err
	}
	if !streamed {
		onProse(answer)
	}
	s.saveAssistantReply(sessionID, answer, stats, time.Since(start).Milliseconds())
	return answer, nil
}

// deviceSessionID gives every device a stable chat session, so its
// conversation continues across reconnects and shows up in the web UI.
func deviceSessionID(device string) string {
	var b strings.Builder
	b.WriteString("device-")
	for _, c := range strings.ToLower(device) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == len("device-") {
		b.WriteString("unnamed")
	}
	return b.String()
}

var (
	speechImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	speechLink    = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s")']+)\)`)
	speechBold    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	speechHeading = regexp.MustCompile(`(?m)^#{1,3} (.+)$`)
	speechMathBlk = regexp.MustCompile(`\$\$([^$]+)\$\$`)
	speechMath    = regexp.MustCompile(`\$([^$\n]+)\$`)
	speechSpace   = regexp.MustCompile(`\s+`)
)

// stripMarkdownForSpeech is the Go twin of index.html's function of the same
// name: the markup the chat UI renders shouldn't be read out loud.
func stripMarkdownForSpeech(value string) string {
	value = speechImage.ReplaceAllString(value, "")
	value = speechLink.ReplaceAllString(value, "$1")
	value = speechBold.ReplaceAllString(value, "$1")
	value = speechHeading.ReplaceAllString(value, "$1")
	value = speechMathBlk.ReplaceAllString(value, "$1")
	value = speechMath.ReplaceAllString(value, "$1")
	return strings.TrimSpace(speechSpace.ReplaceAllString(value, " "))
}

// pcm16ToWAV wraps raw mono little-endian pcm16 in a WAV header.
func pcm16ToWAV(pcm []byte, rate int) []byte {
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// wavToDevicePCM turns a TTS WAV into the device's 16 kHz mono pcm16. A WAV
// already in that format is unwrapped directly; anything else (Piper voices
// are typically 22.05 kHz) is resampled with ffmpeg, like convertToWAV.
func wavToDevicePCM(ctx context.Context, wav []byte) ([]byte, error) {
	if pcm, ok := plainPCM16(wav, deviceSampleRate); ok {
		return pcm, nil
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-f", "s16le", "-acodec", "pcm_s16le", "-ar", "16000", "-ac", "1", "pipe:1")
	cmd.Stdin = bytes.NewReader(wav)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// plainPCM16 returns the samples of a canonical mono pcm16 WAV at `rate`.
func plainPCM16(wav []byte, rate int) ([]byte, bool) {
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, false
	}
	var fmtOK bool
	for off := 12; off+8 <= len(wav); {
		id := string(wav[off : off+4])
		size := int(binary.LittleEndian.Uint32(wav[off+4 : off+8]))
		body := off + 8
		if body+size > len(wav) {
			size = len(wav) - body // tolerate streamed WAVs with a bogus size
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, false
			}
			f := wav[body:]
			fmtOK = binary.LittleEndian.Uint16(f[0:2]) == 1 && binary.LittleEndian.Uint16(f[2:4]) == 1 &&
				binary.LittleEndian.Uint32(f[4:8]) == uint32(rate) && binary.LittleEndian.Uint16(f[14:16]) == 16
		case "data":
			if !fmtOK {
				return nil, false
			}
			return wav[body : body+size], true
		}
		off = body + size + size%2
	}
	return nil, false
}
