package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"kai/internal/harness/dsml"
	"kai/internal/tools"
)

// Config sets up an Agent.
type Config struct {
	Provider    Provider
	Tools       *tools.Registry
	System      string        // Kai's own instructions, sent with the first message
	Thinking    bool          // ask the model to reason
	MaxSteps    int           // model turns per Run (default 30)
	MaxRepairs  int           // retries for unreadable tool calls per Run (default 2)
	ToolTimeout time.Duration // per tool call (default 60s)
	Record      func(Entry)   // called for every history entry (session log); may be nil
}

// Entry is one item of Kai's own record of the conversation (context #2).
type Entry struct {
	Role string    `json:"role"` // "user", "assistant" or "tool"
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Agent runs requests against a provider. It keeps the chat's position so
// that, after the first message, only new content is sent: the web chat keeps
// the rest of the history itself.
type Agent struct {
	cfg Config

	mu      sync.Mutex
	session string // one TUI session = one web chat; changes on Reset or a lost chat
	parent  *int
	started bool   // the current web chat has been given Kai's instructions
	recap   string // recap to put in front of the next first message, after a lost chat
	history []Entry
}

func New(cfg Config) *Agent {
	if cfg.Tools == nil {
		cfg.Tools = tools.NewRegistry()
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 30
	}
	if cfg.MaxRepairs <= 0 {
		cfg.MaxRepairs = 2
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 60 * time.Second
	}
	return &Agent{cfg: cfg, session: newSessionID()}
}

func newSessionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Reset forgets the chat so the next Run starts a new one.
func (a *Agent) Reset(ctx context.Context) {
	a.mu.Lock()
	a.parent, a.started, a.recap, a.history = nil, false, "", nil
	a.session = newSessionID()
	sess := a.session
	a.mu.Unlock()
	slog.Info("agent reset: next message opens a new web chat", "session", sess)
	if s, ok := a.cfg.Provider.(Sessioner); ok {
		_ = s.NewSession(ctx)
	}
}

// History returns Kai's own record of the conversation so far.
func (a *Agent) History() []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Entry(nil), a.history...)
}

func (a *Agent) record(role, text string) {
	e := Entry{Role: role, Text: text, At: time.Now()}
	a.mu.Lock()
	a.history = append(a.history, e)
	a.mu.Unlock()
	if a.cfg.Record != nil {
		a.cfg.Record(e)
	}
}

func (a *Agent) parentID() *int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.parent
}

// setParent is called when a reply begins: the chat now exists, so later
// messages continue it instead of repeating the instructions.
func (a *Agent) setParent(id int) {
	a.mu.Lock()
	a.parent = &id
	a.started = true
	a.recap = ""
	a.mu.Unlock()
}

// markStarted is called as soon as the browser reports that the prompt was
// delivered to the web chat. From then on the chat holds Kai's instructions,
// even if the reply itself fails or never reaches Kai.
func (a *Agent) markStarted() {
	a.mu.Lock()
	first := !a.started
	a.started = true
	a.recap = ""
	sess := a.session
	a.mu.Unlock()
	if first {
		slog.Info("web chat started: instructions sent once, later messages send only new content", "session", sess)
	}
}

// chatState is what the next Turn needs to know.
func (a *Agent) chatState() (parent *int, session string, newChat bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.parent, a.session, !a.started
}

// Run handles one user request. It streams Events and closes the channel when
// the request is finished or ctx is cancelled. Call it again only after the
// channel has closed.
func (a *Agent) Run(ctx context.Context, user string) <-chan Event {
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		a.loop(ctx, user, func(e Event) bool {
			select {
			case out <- e:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	return out
}

// prepare wraps body for sending. The first message of a chat also carries
// Kai's instructions and the tool block (the web chat has no system role).
// After a lost chat it carries a recap of the conversation too.
func (a *Agent) prepare(body string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return body
	}
	if a.recap != "" {
		body = "The earlier chat was lost, so this is a fresh chat. Recap of the conversation so far:\n\n" +
			a.recap + "\n\nContinue from here.\n\n" + body
	}
	return dsml.FirstPrompt(a.cfg.System, a.cfg.Tools.Specs(), body)
}

// restartChat drops the web chat's position and prepares a recap from Kai's
// own record, so the next message opens a new chat that knows what happened.
func (a *Agent) restartChat() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.parent, a.started = nil, false
	a.session = newSessionID()
	a.recap = recap(a.history, recapBudget)
	slog.Warn("web chat lost: starting a new chat with a recap", "session", a.session)
}

func (a *Agent) loop(ctx context.Context, user string, emit func(Event) bool) {
	a.record("user", user)
	body := user // what to say next, without Kai's instructions
	repairs := 0
	recovered := false

	for step := 0; step < a.cfg.MaxSteps; step++ {
		res, err := a.turn(ctx, a.prepare(body), emit)
		if ctx.Err() != nil {
			return // stopped by the user
		}
		if errors.Is(err, ErrChatLost) && !recovered {
			recovered = true // once per request, so a broken bridge cannot loop forever
			a.restartChat()
			step-- // this attempt does not count
			continue
		}
		if err != nil {
			slog.Error("agent turn failed", "step", step, "err", err)
			emit(Event{Kind: EvError, Err: err})
			return
		}
		if res.text != "" {
			a.record("assistant", res.text)
		}

		if len(res.calls) == 0 {
			if res.problem == "" {
				emit(Event{Kind: EvDone})
				return
			}
			if repairs >= a.cfg.MaxRepairs {
				emit(Event{Kind: EvError, Err: fmt.Errorf("the model kept sending unreadable tool calls: %s", res.problem)})
				return
			}
			repairs++
			body = dsml.RepairPrompt(res.problem)
			continue
		}

		results := make([]tools.Result, 0, len(res.calls))
		for i := range res.calls {
			c := res.calls[i]
			if !emit(Event{Kind: EvToolCall, Call: &c}) {
				return
			}
			r := a.cfg.Tools.Execute(ctx, c, a.cfg.ToolTimeout)
			if ctx.Err() != nil {
				return
			}
			if !emit(Event{Kind: EvToolResult, Call: &c, Result: &r}) {
				return
			}
			a.record("tool", r.Output)
			results = append(results, r)
		}
		body = dsml.EncodeResults(results)
	}
	emit(Event{Kind: EvError, Err: fmt.Errorf("stopped after %d steps without a final answer", a.cfg.MaxSteps)})
}

type turnResult struct {
	text    string       // visible text of the reply
	calls   []tools.Call // tool calls found in it
	problem string       // why the reply's tool call could not be read ("" if fine)
}

// turn sends one prompt and reads the whole reply, forwarding text and
// thinking as they arrive.
func (a *Agent) turn(ctx context.Context, prompt string, emit func(Event) bool) (turnResult, error) {
	var res turnResult

	parent, session, newChat := a.chatState()
	slog.Info("agent turn", "session", session, "newChat", newChat, "hasParent", parent != nil, "promptBytes", len(prompt))
	stream, err := a.cfg.Provider.Send(ctx, Turn{
		Prompt:   prompt,
		ParentID: parent,
		Thinking: a.cfg.Thinking,
		Session:  session,
		NewChat:  newChat,
	})
	if err != nil {
		return res, err
	}

	var text strings.Builder
	show := func(s string) bool {
		if s == "" {
			return true
		}
		text.WriteString(s)
		return emit(Event{Kind: EvText, Text: s})
	}

	p := dsml.NewParser()
	ended := false

read:
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case ev, ok := <-stream:
			if !ok {
				break read
			}
			switch ev.Kind {
			case StreamSent:
				a.markStarted()
			case StreamReady:
				a.setParent(ev.MessageID)
			case StreamThinking:
				if !emit(Event{Kind: EvThinking, Text: ev.Text}) {
					return res, ctx.Err()
				}
			case StreamText:
				if !show(p.Feed(ev.Text)) {
					return res, ctx.Err()
				}
			case StreamEnd:
				ended = true
				if st := ev.Text; st != "" && st != "FINISHED" {
					return res, fmt.Errorf("the reply ended early (status %s)", st)
				}
			case StreamError:
				if !ended {
					if ev.Err == nil {
						return res, errors.New("the stream failed")
					}
					return res, ev.Err
				}
			}
		}
	}

	if !ended {
		return res, errors.New("the reply stream closed before the reply was finished")
	}
	if !show(p.Finish()) {
		return res, ctx.Err()
	}

	res.text = text.String()
	res.calls = p.Calls()
	if len(res.calls) == 0 && len(p.Warnings()) > 0 {
		res.problem = strings.Join(p.Warnings(), "; ")
	}
	return res, nil
}
