package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"kai/internal/harness"
	"kai/internal/harness/provider/deepseek"
	"kai/internal/harness/provider/qwen"
)

// The web chats Kai can drive. The extension opens the matching site; the
// reply stream is decoded by the matching decoder.
const (
	SiteDeepSeek = "deepseek"
	SiteQwen     = "qwen"
)

// siteHost is the address the person sees in messages about a site.
func siteHost(site string) string {
	if site == SiteQwen {
		return "chat.qwen.ai"
	}
	return "chat.deepseek.com"
}

func decoderFor(site string) func(context.Context, io.Reader) <-chan harness.StreamEvent {
	if site == SiteQwen {
		return qwen.Decode
	}
	return deepseek.Decode
}

var (
	// ErrNotConnected is returned when no extension is polling.
	ErrNotConnected = errors.New("the Kai Bridge extension is not connected: install it in Chrome (chrome://extensions, Load unpacked), open chat.deepseek.com or chat.qwen.ai and log in. Details are in logs.txt")

	errNotPickedUp = errors.New("the extension never picked up the message: is the Kai Bridge extension enabled? Details are in logs.txt")
	errNotSent     = errors.New("the extension got the message but could not send it in the chat tab: is it logged in? Details are in logs.txt")
	errNoReply     = errors.New("the message was sent in the browser but no reply reached Kai: open the extension popup, run Self-test, and check logs.txt")
	errStalled     = errors.New("the reply stalled: no data for two minutes")
)

const (
	pollWait        = 20 * time.Second
	connectedWindow = 35 * time.Second
	maxEventBody    = 16 << 20
	maxLogBody      = 1 << 20

	// Each stage of a send has its own deadline, so a failure says where it
	// got stuck. Qwen can take a while to finish a previous answer, enable its
	// send button and start the request, so the send stage is generous.
	acceptTimeout     = 15 * time.Second  // extension must pick the command up
	sentTimeout       = 180 * time.Second // extension must get the prompt into the chat
	firstChunkTimeout = 120 * time.Second // the chat must start streaming
	idleTimeout       = 150 * time.Second

	// ClientHeader is sent by the extension on every request. A web page cannot
	// add it to a cross-origin request without a preflight, which this server
	// never answers, so pages cannot talk to the bridge.
	ClientHeader = "X-Kai-Client"
)

const (
	stageQueued    = "queued"
	stageAccepted  = "accepted"
	stageSent      = "sent"
	stageStreaming = "streaming"
)

// Command is what Kai asks the extension to do.
type Command struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // "send" or "cancel"
	Prompt  string `json:"prompt,omitempty"`
	Session string `json:"session,omitempty"` // one Kai session = one web chat
	NewChat bool   `json:"newChat,omitempty"` // first message of the session: open a fresh chat
	Site    string `json:"site,omitempty"`    // "deepseek" (default) or "qwen": which web chat to use
}

// event is what the extension reports back.
type event struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // "accepted", "sent", "chunk", "end", "error", "chat_lost"
	Data    string `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
}

type logLine struct {
	T   int64  `json:"t"`
	Src string `json:"src"`
	Lvl string `json:"lvl"`
	Msg string `json:"msg"`
}

// stream is one reply in flight: raw SSE goes in through pw and comes out
// decoded on the harness side.
type stream struct {
	id    string
	pw    *io.PipeWriter
	log   *slog.Logger
	began time.Time

	sent     chan struct{} // closed when the browser reports the prompt was sent
	sentOnce sync.Once
	done     chan struct{}
	once     sync.Once

	mu     sync.Mutex
	timer  *time.Timer
	stage  string
	chunks int
	bytes  int
}

func (st *stream) advance(from, to string, d time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stage != from {
		return // an out-of-order event: ignore
	}
	st.stage = to
	st.timer.Reset(d)
}

func (st *stream) markSent() {
	st.mu.Lock()
	if st.stage == stageQueued || st.stage == stageAccepted {
		st.stage = stageSent
		st.timer.Reset(firstChunkTimeout)
	}
	st.mu.Unlock()
	st.sentOnce.Do(func() { close(st.sent) })
}

func (st *stream) onTimeout() {
	st.mu.Lock()
	stage := st.stage
	st.mu.Unlock()
	switch stage {
	case stageQueued:
		st.finish(errNotPickedUp)
	case stageAccepted:
		st.finish(errNotSent)
	case stageStreaming:
		st.finish(errStalled)
	default:
		st.finish(errNoReply)
	}
}

func (st *stream) finish(err error) {
	st.once.Do(func() {
		st.timer.Stop()
		st.mu.Lock()
		stage, chunks, bytes := st.stage, st.chunks, st.bytes
		st.mu.Unlock()
		if err != nil {
			st.log.Warn("reply failed", "id", st.id, "stage", stage, "chunks", chunks, "bytes", bytes,
				"after", time.Since(st.began).Round(time.Millisecond), "err", err)
			st.pw.CloseWithError(err)
		} else {
			st.log.Info("reply complete", "id", st.id, "chunks", chunks, "bytes", bytes,
				"after", time.Since(st.began).Round(time.Millisecond))
			st.pw.Close()
		}
		close(st.done)
	})
}

// Server is the bridge. It implements harness.Provider.
type Server struct {
	log  *slog.Logger
	cmds chan Command

	mu       sync.Mutex
	streams  map[string]*stream
	lastPoll time.Time
	wasConn  bool
}

func NewServer(log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{log: log, cmds: make(chan Command, 16), streams: map[string]*stream{}}
}

// Start listens on addr (for example 127.0.0.1:8765) and serves in the
// background.
func Start(addr string, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := NewServer(log)
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	s.log.Info("bridge listening", "addr", addr)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/poll", s.guard(s.handlePoll))
	mux.HandleFunc("POST /v1/event", s.guard(s.handleEvent))
	mux.HandleFunc("POST /v1/log", s.guard(s.handleLog))
	mux.HandleFunc("GET /v1/status", s.guard(s.handleStatus))
	return mux
}

// guard rejects anything that is not the extension: wrong Host (DNS
// rebinding), a web page's Origin, or a request without the client header.
// There is no token to type in: the bridge only listens on localhost.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			s.log.Warn("rejected request: bad host", "host", r.Host, "path", r.URL.Path)
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "chrome-extension://") {
			s.log.Warn("rejected request: bad origin", "origin", o, "path", r.URL.Path)
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Header.Get(ClientHeader) == "" {
			s.log.Warn("rejected request: missing client header", "path", r.URL.Path)
			http.Error(w, "forbidden client", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (s *Server) touch() {
	s.mu.Lock()
	was := s.wasConn
	s.lastPoll = time.Now()
	s.wasConn = true
	s.mu.Unlock()
	if !was {
		s.log.Info("extension connected")
	}
}

// Connected reports whether an extension has polled recently.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := !s.lastPoll.IsZero() && time.Since(s.lastPoll) < connectedWindow
	if !ok && s.wasConn {
		s.wasConn = false
		s.log.Warn("extension disconnected (no poll for a while)")
	}
	return ok
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"connected": s.Connected()})
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	s.touch()
	select {
	case c := <-s.cmds:
		s.touch()
		if r.Context().Err() != nil {
			// The extension went away (its worker was suspended) while we
			// were handing it the command: keep the command for the next poll.
			s.log.Warn("poller vanished; requeueing command", "id", c.ID, "type", c.Type)
			select {
			case s.cmds <- c:
			default:
			}
			return
		}
		s.log.Info("command delivered to extension", "id", c.ID, "type", c.Type)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c)
	case <-time.After(pollWait):
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	var ev event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEventBody)).Decode(&ev); err != nil {
		s.log.Warn("bad event from extension", "err", err)
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	s.deliver(ev)
	w.WriteHeader(http.StatusNoContent)
}

// handleLog takes log lines from the extension and writes them into Kai's own
// log, so one file (logs.txt) shows both sides.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lines []logLine `json:"lines"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLogBody)).Decode(&body); err != nil {
		http.Error(w, "bad log", http.StatusBadRequest)
		return
	}
	for _, l := range body.Lines {
		lvl := slog.LevelInfo
		switch strings.ToLower(l.Lvl) {
		case "debug":
			lvl = slog.LevelDebug
		case "warn", "warning":
			lvl = slog.LevelWarn
		case "error":
			lvl = slog.LevelError
		}
		s.log.Log(context.Background(), lvl, "ext: "+l.Msg, "src", l.Src, "at", time.UnixMilli(l.T).Format("15:04:05.000"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func preview(s string, n int) string {
	if len(s) > n {
		s = s[:n] + "..."
	}
	return strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(s)
}

func (s *Server) deliver(ev event) {
	s.mu.Lock()
	st := s.streams[ev.ID]
	s.mu.Unlock()
	if st == nil {
		s.log.Warn("event for unknown or finished request: dropped", "id", ev.ID, "type", ev.Type)
		return
	}
	switch ev.Type {
	case "accepted":
		st.advance(stageQueued, stageAccepted, sentTimeout)
		s.log.Info("extension accepted the message", "id", ev.ID)
	case "sent":
		st.markSent()
		s.log.Info("message delivered to the web chat", "id", ev.ID, "after", time.Since(st.began).Round(time.Millisecond))
	case "chunk":
		st.mu.Lock()
		first := st.chunks == 0
		st.chunks++
		st.bytes += len(ev.Data)
		st.stage = stageStreaming
		st.timer.Reset(idleTimeout)
		st.mu.Unlock()
		if first {
			s.log.Info("first reply data received", "id", ev.ID, "after", time.Since(st.began).Round(time.Millisecond),
				"bytes", len(ev.Data), "preview", preview(ev.Data, 400))
		}
		// Blocks until the decoder has read it, which gives the extension
		// backpressure. A closed pipe just means the reader gave up.
		_, _ = st.pw.Write([]byte(ev.Data))
	case "end":
		st.finish(nil)
	case "chat_lost":
		s.log.Warn("extension lost the web chat", "id", ev.ID, "why", ev.Message)
		st.finish(harness.ErrChatLost)
	case "error":
		msg := ev.Message
		if msg == "" {
			msg = "the browser reported an error"
		}
		s.log.Error("browser reported an error", "id", ev.ID, "message", msg)
		st.finish(errors.New(msg))
	default:
		s.log.Warn("unknown event type", "id", ev.ID, "type", ev.Type)
	}
}

// Send implements harness.Provider for DeepSeek Web.
func (s *Server) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	return s.send(ctx, t, SiteDeepSeek)
}

// ForSite returns a provider that talks to the given web chat through this
// same bridge: one port and one extension serve every site.
func (s *Server) ForSite(site string) harness.Provider { return siteProvider{s: s, site: site} }

type siteProvider struct {
	s    *Server
	site string
}

func (p siteProvider) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	return p.s.send(ctx, t, p.site)
}

// ToolProtocol selects the text format the chat is taught. Qwen Web cannot
// emit native API tool_calls through the page, so it uses the strict JSON
// protocol; DeepSeek Web keeps its DSML markup.
func (p siteProvider) ToolProtocol() harness.ToolProtocol {
	if p.site == SiteQwen {
		return harness.ProtocolJSON
	}
	return harness.ProtocolDSML
}

// send asks the extension to send t.Prompt in the site's chat and returns the
// decoded reply stream. A Turn with NewChat set opens a fresh web chat; later
// turns of the same Session continue it.
func (s *Server) send(ctx context.Context, t harness.Turn, site string) (<-chan harness.StreamEvent, error) {
	if !s.Connected() {
		s.log.Warn("send refused: extension not connected")
		return nil, ErrNotConnected
	}
	id, err := randomHex(8)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	st := &stream{
		id:    id,
		pw:    pw,
		log:   s.log,
		began: time.Now(),
		sent:  make(chan struct{}),
		done:  make(chan struct{}),
		stage: stageQueued,
	}
	st.timer = time.AfterFunc(acceptTimeout, st.onTimeout)

	s.mu.Lock()
	s.streams[id] = st
	s.mu.Unlock()

	cmd := Command{ID: id, Type: "send", Prompt: t.Prompt, Session: t.Session, NewChat: t.NewChat, Site: site}
	select {
	case s.cmds <- cmd:
	default:
		st.finish(errors.New("the bridge is busy"))
		s.forget(id)
		return nil, errors.New("the bridge is busy: too many pending commands")
	}
	s.log.Info("message queued for the extension", "id", id, "site", siteHost(site), "session", t.Session, "newChat", t.NewChat, "promptBytes", len(t.Prompt))

	go func() {
		select {
		case <-ctx.Done():
			s.log.Info("request cancelled", "id", id)
			st.finish(ctx.Err())
			select {
			case s.cmds <- Command{ID: id, Type: "cancel"}:
			default:
			}
		case <-st.done:
		}
		s.forget(id)
	}()

	return merge(ctx, st, decoderFor(site)(ctx, pr)), nil
}

// merge puts a StreamSent event in front of the decoded reply as soon as the
// browser reports the prompt was delivered, so the agent knows the web chat
// exists even if the reply never arrives.
func merge(ctx context.Context, st *stream, dec <-chan harness.StreamEvent) <-chan harness.StreamEvent {
	out := make(chan harness.StreamEvent, 64)
	go func() {
		defer close(out)
		emit := func(e harness.StreamEvent) bool {
			select {
			case out <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		sentCh := st.sent // set to nil once forwarded, so the case stops firing
		forward := func() bool {
			if sentCh == nil {
				return true
			}
			select {
			case <-sentCh:
				sentCh = nil
				return emit(harness.StreamEvent{Kind: harness.StreamSent})
			default:
				return true
			}
		}
		for {
			select {
			case <-sentCh:
				sentCh = nil
				if !emit(harness.StreamEvent{Kind: harness.StreamSent}) {
					return
				}
			case e, ok := <-dec:
				if !ok {
					forward()
					return
				}
				if !forward() || !emit(e) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (s *Server) forget(id string) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
