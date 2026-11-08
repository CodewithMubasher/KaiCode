package bridge

import (
	"context"
	"crypto/subtle"
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
)

var (
	// ErrNotConnected is returned when no extension is polling.
	ErrNotConnected = errors.New("the Kai Chrome extension is not connected: open chat.deepseek.com in Chrome and check the extension popup (address and token)")

	errNoReply = errors.New("the browser did not start a reply in time: is chat.deepseek.com open and logged in?")
)

const (
	pollWait          = 20 * time.Second
	connectedWindow   = 35 * time.Second
	firstChunkTimeout = 90 * time.Second
	maxEventBody      = 16 << 20
)

// Command is what Kai asks the extension to do.
type Command struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // "send" or "cancel"
	Prompt  string `json:"prompt,omitempty"`
	NewChat bool   `json:"newChat,omitempty"` // open a fresh chat first
}

// event is what the extension reports back.
type event struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // "chunk", "end", "error", "chat_lost"
	Data    string `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
}

// stream is one reply in flight: raw SSE goes in through pw and comes out
// decoded on the harness side.
type stream struct {
	pw    *io.PipeWriter
	timer *time.Timer // fires if the browser never starts the reply
	done  chan struct{}
	once  sync.Once
}

func (st *stream) finish(err error) {
	st.once.Do(func() {
		st.timer.Stop()
		if err != nil {
			st.pw.CloseWithError(err)
		} else {
			st.pw.Close()
		}
		close(st.done)
	})
}

// Server is the bridge. It implements harness.Provider.
type Server struct {
	token string
	log   *slog.Logger
	cmds  chan Command

	mu       sync.Mutex
	streams  map[string]*stream
	lastPoll time.Time
}

func NewServer(token string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{token: token, log: log, cmds: make(chan Command, 16), streams: map[string]*stream{}}
}

// Start listens on addr (for example 127.0.0.1:8765) and serves in the
// background.
func Start(addr, token string, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := NewServer(token, log)
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	s.log.Info("bridge listening", "addr", addr)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/poll", s.guard(s.handlePoll))
	mux.HandleFunc("POST /v1/event", s.guard(s.handleEvent))
	mux.HandleFunc("GET /v1/status", s.guard(s.handleStatus))
	return mux
}

// guard rejects anything that is not the extension: wrong Host (DNS
// rebinding), a web page's Origin, or a bad token.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "chrome-extension://") {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Kai-Token")), []byte(s.token)) != 1 {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastPoll = time.Now()
	s.mu.Unlock()
}

// Connected reports whether an extension has polled recently.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.lastPoll.IsZero() && time.Since(s.lastPoll) < connectedWindow
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
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	s.deliver(ev)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deliver(ev event) {
	s.mu.Lock()
	st := s.streams[ev.ID]
	s.mu.Unlock()
	if st == nil {
		return // cancelled or unknown: drop
	}
	switch ev.Type {
	case "chunk":
		st.timer.Stop()
		// Blocks until the decoder has read it, which gives the extension
		// backpressure. A closed pipe just means the reader gave up.
		_, _ = st.pw.Write([]byte(ev.Data))
	case "end":
		st.finish(nil)
	case "chat_lost":
		st.finish(harness.ErrChatLost)
	case "error":
		msg := ev.Message
		if msg == "" {
			msg = "the browser reported an error"
		}
		st.finish(errors.New(msg))
	}
}

// Send implements harness.Provider: it asks the extension to send t.Prompt in
// the DeepSeek chat and returns the decoded reply stream. A Turn with no
// ParentID starts a new chat.
func (s *Server) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	if !s.Connected() {
		return nil, ErrNotConnected
	}
	id, err := randomHex(8)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	st := &stream{pw: pw, done: make(chan struct{})}
	st.timer = time.AfterFunc(firstChunkTimeout, func() { st.finish(errNoReply) })

	s.mu.Lock()
	s.streams[id] = st
	s.mu.Unlock()

	select {
	case s.cmds <- Command{ID: id, Type: "send", Prompt: t.Prompt, NewChat: t.ParentID == nil}:
	default:
		st.finish(errors.New("the bridge is busy"))
		s.forget(id)
		return nil, errors.New("the bridge is busy: too many pending commands")
	}

	go func() {
		select {
		case <-ctx.Done():
			st.finish(ctx.Err())
			select {
			case s.cmds <- Command{ID: id, Type: "cancel"}:
			default:
			}
		case <-st.done:
		}
		s.forget(id)
	}()

	return deepseek.Decode(ctx, pr), nil
}

func (s *Server) forget(id string) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}
