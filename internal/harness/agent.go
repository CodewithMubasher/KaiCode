package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"kai/internal/tools"
)

// defaultMaxSteps is how many model turns one request may take. Long coding
// tasks read, write, build and fix for many rounds; with parallel tool calls
// each round does much more, but the ceiling still has to be high.
const defaultMaxSteps = 200

// progressEvery is the longest a growing line count waits before it is shown.
const progressEvery = 60 * time.Millisecond

// Config sets up an Agent. Set Provider (DSML web chat) or Native (API tool
// calling), not both: they are two separate paths. If both are set, Native wins.
type Config struct {
	Provider Provider       // DSML path: DeepSeek Web
	Native   NativeProvider // native path: OpenAI-compatible APIs
	Tools    *tools.Registry

	// System is Kai's instructions for the DSML path. It travels inside the
	// first message, next to the DSML tool block.
	System string
	// NativeSystem is Kai's instructions for the native path. It is the
	// API's system message; it never contains tool-call markup, because the
	// tools travel as structured definitions. If empty, System is used.
	NativeSystem string

	Thinking        bool          // DSML path: ask the web chat to reason
	MaxSteps        int           // model turns per Run (default 200)
	MaxRepairs      int           // web path: retries for unreadable tool calls per Run (default 3)
	MaxContextBytes int           // native path: conversation size kept and sent (default 600 KB, about 150k tokens)
	ToolTimeout     time.Duration // per tool call (default 60s)
	Record          func(Entry)   // called for every history entry (session log); may be nil
}

// Entry is one item of Kai's own record of the conversation (context #2).
type Entry struct {
	Role   string    `json:"role"`
	Text   string    `json:"text"`
	Steps  string    `json:"steps,omitempty"`
	Tokens int       `json:"tokens,omitempty"`
	At     time.Time `json:"at"`
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
	tokens  int // estimate of what the current web chat holds

	// Native path only. The API is stateless, so the agent owns the
	// conversation and sends it all every time. The system message is not
	// stored here; it is put in front when the request is built.
	msgs []ChatMessage
}

func New(cfg Config) *Agent {
	if cfg.Tools == nil {
		cfg.Tools = tools.NewRegistry()
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = defaultMaxSteps
	}
	if cfg.MaxRepairs <= 0 {
		cfg.MaxRepairs = 3
	}
	if cfg.MaxContextBytes <= 0 {
		cfg.MaxContextBytes = defaultMaxContextBytes
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
	a.msgs = nil
	a.tokens = 0
	a.session = newSessionID()
	sess := a.session
	a.mu.Unlock()
	slog.Info("agent reset: next message opens a new chat", "session", sess)
	if s, ok := a.provider().(Sessioner); ok {
		_ = s.NewSession(ctx)
	}
}

// History returns Kai's own record of the conversation so far.
func (a *Agent) History() []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Entry(nil), a.history...)
}

// Snapshot is the agent state that cannot be rebuilt from the message log.
// The native path is stateless, so its full conversation (tool calls and
// results included) must be saved to continue it after a restart; the token
// count is the provider's last real count.
type Snapshot struct {
	Tokens int           `json:"tokens,omitempty"`
	Native []ChatMessage `json:"native,omitempty"`
}

// Snapshot returns the current agent state for storage.
func (a *Agent) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{Tokens: a.tokens, Native: cloneMessages(a.msgs)}
}

func cloneMessages(in []ChatMessage) []ChatMessage {
	if len(in) == 0 {
		return nil
	}
	out := make([]ChatMessage, len(in))
	copy(out, in)
	for i := range out {
		out[i].ToolCalls = append([]ToolCall(nil), in[i].ToolCalls...)
	}
	return out
}

func (a *Agent) record(role, text, steps string) {
	e := Entry{Role: role, Text: text, Steps: steps, At: time.Now()}
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

// addTokens records what the current web chat now holds. The web chat keeps
// the history itself, so this is every prompt Kai sent plus every reply the
// model wrote, including thinking and the tool-call markup.
func (a *Agent) addTokens(n int) {
	a.mu.Lock()
	a.tokens += n
	a.mu.Unlock()
}

func (a *Agent) tokensNow() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens
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
		emit := func(e Event) bool {
			select {
			case out <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		// Two separate paths; the backend decides which one runs.
		if a.nativeProvider() != nil {
			a.loopNative(ctx, user, emit)
		} else {
			a.loopWeb(ctx, user, emit)
		}
	}()
	return out
}

// provider is the provider in use right now (it can change with SetProvider).
func (a *Agent) provider() Provider {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Provider
}

// SetProvider switches to a DSML web-chat provider in the middle of a
// conversation. Kai's own record of the conversation is kept: the next message
// opens a fresh chat on the new provider and starts with a recap of it.
func (a *Agent) SetProvider(p Provider) {
	a.SetBackend(Backend{Web: p})
}

// SetNative switches to a native tool-calling provider, keeping the record
// of the conversation like SetProvider does.
func (a *Agent) SetNative(p NativeProvider) {
	a.SetBackend(Backend{Native: p})
}

// SetBackend switches the path and provider. Exactly one of b.Web and
// b.Native should be set; the other path is cleared so the two never mix.
func (a *Agent) SetBackend(b Backend) {
	a.mu.Lock()
	a.cfg.Provider, a.cfg.Native = b.Web, b.Native
	a.parent, a.started = nil, false
	a.msgs = nil
	a.tokens = 0
	a.session = newSessionID()
	a.recap = recap(a.history, recapBudget)
	sess := a.session
	a.mu.Unlock()
	slog.Info("provider switched: next message opens a new chat with a recap", "session", sess, "native", b.Native != nil)
	if s, ok := b.Web.(Sessioner); ok {
		_ = s.NewSession(context.Background())
	}
}

// Resume loads a saved conversation. Kai's record becomes the history and the
// next message opens a fresh chat that starts with a recap of it.
func (a *Agent) Resume(h []Entry) {
	a.mu.Lock()
	a.parent, a.started = nil, false
	a.msgs = nil
	a.tokens = 0
	a.session = newSessionID()
	a.history = append([]Entry(nil), h...)
	a.recap = recap(a.history, recapBudget)
	sess := a.session
	a.mu.Unlock()
	slog.Info("session resumed: next message opens a new chat with a recap", "session", sess, "entries", len(h))
	if s, ok := a.provider().(Sessioner); ok {
		_ = s.NewSession(context.Background())
	}
}

// ResumeWithState loads a saved conversation and the state saved with it. On
// the native path the full conversation is restored and sent as it was, so
// the model keeps its context exactly; on the web path the browser chat is
// gone, so the recap is still used, but the saved token count is kept.
func (a *Agent) ResumeWithState(h []Entry, s Snapshot) {
	a.Resume(h)
	a.mu.Lock()
	if s.Tokens > 0 {
		a.tokens = s.Tokens
	}
	if a.cfg.Native != nil && len(s.Native) > 0 {
		a.msgs = cloneMessages(s.Native)
		a.recap = ""
		a.started = true
	}
	a.mu.Unlock()
}

// nativeProvider is the native provider in use, or nil on the DSML path.
func (a *Agent) nativeProvider() NativeProvider {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Native
}

// progressGate decides when a growing tool-call progress is worth reporting.
// A new file or a change of kind is reported at once; a growing line count at
// most every progressEvery, so a long file does not flood the UI with events.
// Both paths use it.
type progressGate struct {
	sent   *ToolProgress // last progress reported
	sentAt time.Time     // when it was reported
	latest *ToolProgress // newest progress seen, reported or not
}

// offer takes the newest progress and reports whether to show it now.
func (g *progressGate) offer(hp *ToolProgress) bool {
	g.latest = hp
	changed := g.sent == nil || g.sent.Name != hp.Name || g.sent.Path != hp.Path || g.sent.Creating != hp.Creating
	// Growth of an eighth also counts, so a short file still shows its count
	// climbing even on a very fast stream.
	grew := !changed && hp.Lines-g.sent.Lines >= 1+g.sent.Lines/8
	if changed || grew || (g.sent.Lines != hp.Lines && time.Since(g.sentAt) >= progressEvery) {
		g.sent, g.sentAt = hp, time.Now()
		return true
	}
	return false
}

// pending is the final count the throttle held back, or nil if none.
func (g *progressGate) pending() *ToolProgress {
	if g.latest != nil && (g.sent == nil || *g.latest != *g.sent) {
		return g.latest
	}
	return nil
}

// stopAfterSame is how many identical failing calls in a row end a request:
// a model that sends the same broken call four times is not going to fix it.
const stopAfterSame = 4

// failGuard watches one request for a model that keeps repeating a failing
// call. It adds a firm instruction to the failing result (which the model
// reads) and tells the loop when to give up. Both paths use it.
type failGuard struct {
	lastKey string
	same    int            // identical call, failed this many times in a row
	byTool  map[string]int // failures in a row per tool, whatever the arguments
}

// note looks at a finished call. It may extend r.Output with guidance. It
// returns true when the request should stop.
func (g *failGuard) note(reg *tools.Registry, c tools.Call, r *tools.Result) bool {
	if g.byTool == nil {
		g.byTool = map[string]int{}
	}
	if !r.IsError {
		g.byTool[c.Name] = 0
		g.lastKey, g.same = "", 0
		return false
	}
	args, _ := json.Marshal(c.Args) // map keys are sorted: the same call gives the same text
	key := c.Name + " " + string(args)
	if key == g.lastKey {
		g.same++
	} else {
		g.lastKey, g.same = key, 1
	}
	g.byTool[c.Name]++

	usage := ""
	if t, ok := reg.Get(c.Name); ok {
		usage = " Usage: " + t.Spec().Usage() + "."
	}
	switch {
	case g.same >= 2:
		r.Output += fmt.Sprintf("\n\nSTOP: you sent this exact call %d times and it failed the same way each time. "+
			"Sending it again will fail again. Read the error above and change the call, or use a different approach.%s", g.same, usage)
	case g.byTool[c.Name] >= 3:
		r.Output += fmt.Sprintf("\n\nNote: %q has failed %d times in a row. Stop guessing: re-read the error and the usage, "+
			"and try a different approach if this one does not work.%s", c.Name, g.byTool[c.Name], usage)
	}
	return g.same >= stopAfterSame
}

func repeatError(name string) error {
	return fmt.Errorf("the model kept sending the same failing %q call and was stopped; send your request again, or give it more detail", name)
}
