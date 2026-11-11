package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"kai/internal/bridge"
	"kai/internal/config"
	"kai/internal/harness"
	"kai/internal/tools"
	"kai/internal/ui/components"
)

// kaiSystem is Kai's instructions for DeepSeek Web. The web chat has no system
// role, so the harness puts it in the first message, next to the DSML block
// that teaches the model the tool-call format.
func kaiSystem() string {
	return "You are Kai, a coding agent working in the user's project directory.\n\n" + tools.SystemGuide()
}

// kaiSystemNative is Kai's instructions for OpenAI-compatible providers. It is
// the API's system message. The tools are passed as API tool definitions, so
// it says nothing about a call format, and it tells the model not to invent one.
func kaiSystemNative() string {
	return "You are Kai, a coding agent working in the user's project directory.\n\n" + tools.SystemGuide() +
		"\n- Call tools only through the API's tool-calling interface. Never write tool-call markup or tool results as text in your reply."
}

const toolTimeout = 11 * time.Minute

type runState struct {
	agent   *harness.Agent
	events  <-chan harness.Event
	cancel  context.CancelFunc
	id      int
	thinkAt time.Time
	tokens  int

	// pending is the batch of tool calls that have been announced and have
	// not returned yet. Their results are drawn in call order; until then one
	// transient line says what is running.
	pending []pendingCall

	savedTokens int
	savedAt     time.Time
}

type pendingCall struct {
	id    string
	label string
	call  tools.Call
}

type (
	agentMsg struct {
		id int
		ev harness.Event
	}
	agentDoneMsg struct{ id int }
)

func newRunState(cfg *config.Config, ps *persister, hub *providerHub) runState {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.Shell{})
	reg.Register(tools.EditFile{})
	reg.Register(tools.CreateFile{})

	active := activeProvider{Kind: "web"}
	switch {
	case cfg.Provider.Name == "mock":
		active = activeProvider{Kind: "mock"}
	case hub.st != nil:
		if v, ok := hub.st.Setting(settingActive); ok {
			active = parseActive(v)
		}
	}
	backend, label, err := hub.build(active)
	if err != nil {
		slog.Warn("the saved provider is unavailable, using DeepSeek Web", "provider", active.String(), "err", err)
		active = activeProvider{Kind: "web"}
		backend, label, _ = hub.build(active)
	}
	hub.active, hub.label = active, label
	ps.setProvider(label)

	record := ps.record
	if hub.st == nil {
		record = sessionLogger(cfg.Session.Dir)
	}

	return runState{agent: harness.New(harness.Config{
		Provider:     backend.Web,    // DeepSeek Web / Qwen Web: DSML
		Native:       backend.Native, // custom OpenAI-compatible: API tool calling
		Tools:        reg,
		System:       kaiSystem(),
		NativeSystem: kaiSystemNative(),
		ToolTimeout:  toolTimeout,
		Record:       record,
	})}
}

func startBridge(cfg *config.Config) (*bridge.Server, error) {
	addr := cfg.Provider.Addr
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	srv, err := bridge.Start(addr, slog.Default())
	if err != nil {
		slog.Error("could not start the bridge", "addr", addr, "err", err)
		return nil, fmt.Errorf("could not start the bridge on %s (is another Kai running?): %w", addr, err)
	}
	return srv, nil
}

type brokenProvider struct{ err error }

func (b brokenProvider) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	return nil, b.err
}

func sessionLogger(dir string) func(harness.Entry) {
	if dir == "" {
		return nil
	}
	var mu sync.Mutex
	var f *os.File
	return func(e harness.Entry) {
		mu.Lock()
		defer mu.Unlock()
		if f == nil {
			if os.MkdirAll(dir, 0o755) != nil {
				return
			}
			name := filepath.Join(dir, time.Now().Format("20060102-150405")+".jsonl")
			file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return
			}
			f = file
		}
		if b, err := json.Marshal(e); err == nil {
			_, _ = f.Write(append(b, '\n'))
		}
	}
}

func (m Model) startRun(text string) (Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.run.id++
	m.run.cancel = cancel
	m.run.events = m.agent().Run(ctx, text)
	m.run.thinkAt = time.Time{}
	m.run.pending = nil
	return m, waitEvent(m.run.id, m.run.events)
}

func (m Model) agent() *harness.Agent { return m.run.agent }

func (m Model) stopRun() Model {
	if m.run.cancel != nil {
		m.run.cancel()
		m.run.cancel = nil
	}
	m.run.id++
	m.run.events = nil
	m.run.pending = nil
	return m
}

func waitEvent(id int, ch <-chan harness.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return agentDoneMsg{id}
		}
		return agentMsg{id, ev}
	}
}

// tokenSaveEvery bounds how often the token count is written to the database:
// usage events arrive every few tokens and a write on each one stalls the UI.
const tokenSaveEvery = 3 * time.Second

func (m Model) saveTokens(force bool) Model {
	if m.st == nil || m.ps == nil {
		return m
	}
	id := m.ps.current()
	if id == "" || m.run.tokens == m.run.savedTokens {
		return m
	}
	if !force && time.Since(m.run.savedAt) < tokenSaveEvery {
		return m
	}
	if err := m.st.UpdateSessionTokens(id, m.run.tokens); err != nil {
		slog.Warn("could not save the token count", "session", id, "err", err)
	}
	m.run.savedTokens, m.run.savedAt = m.run.tokens, time.Now()
	return m
}

func (m Model) applyEvent(ev harness.Event) Model {
	switch ev.Kind {
	case harness.EvThinking:
		if m.run.thinkAt.IsZero() {
			m.run.thinkAt = time.Now()
		}
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.StreamThinking(ev.Text)

	case harness.EvText:
		m = m.endThinking()
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.StreamAnswer(ev.Text)

	case harness.EvUsage:
		m.run.tokens = ev.Tokens
		m = m.saveTokens(false)

	case harness.EvToolProgress:
		if ev.Progress != nil {
			m = m.endThinking()
			m.chat = m.chat.WriteProgress(ev.Progress.Name, ev.Progress.Path, ev.Progress.Lines, ev.Progress.Creating)
		}

	case harness.EvToolCall:
		m = m.endThinking()
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.DemoteAnswer()
		if ev.Call == nil {
			break
		}
		// Nothing is drawn per call. The call joins the running batch, and
		// its finished block is added, in call order, when its result comes.
		pend := make([]pendingCall, len(m.run.pending), len(m.run.pending)+1)
		copy(pend, m.run.pending)
		m.run.pending = append(pend, pendingCall{id: ev.Call.ID, label: callLabel(*ev.Call), call: *ev.Call})
		m = m.showPending()

	case harness.EvToolResult:
		if ev.Call == nil || ev.Result == nil {
			break
		}
		// Whatever was showing "in progress" ends now. Each result adds
		// exactly one block to the timeline.
		m = m.finishPending(*ev.Call)
		m.chat = m.chat.EndWrite()
		switch {
		case ev.Call.Name == "read_file" && !ev.Result.IsError:
			path, _ := tools.StringArg(ev.Call.Args, "path")
			m.chat = m.chat.ReadFile(path)
		case ev.Call.Name == "edit_file" || ev.Call.Name == "create_file":
			// A file step is a timeline node: the full content of a created
			// file, or the diff of an edit. The tool's own result text (what
			// the model reads) is never drawn.
			path, create, diff, _ := editDisplay(*ev.Call, ev.Result)
			if ev.Result.IsError {
				m.chat = m.chat.EditFailed(path, create, shortError(ev.Result))
			} else {
				m.chat = m.chat.EditFile(path, create, diff)
			}
		default:
			out := ev.Result.Output
			if ev.Result.IsError {
				// A mistake in the call itself (missing parameter, unknown
				// tool) is guidance for the model: the person gets one line.
				if s := strings.TrimSpace(ev.Result.Short); s != "" {
					out = "Error: " + s
				} else {
					out = "Error: " + out
				}
			}
			m.chat = m.chat.RunShell(callLabel(*ev.Call), out)
		}
		m = m.showPending() // the rest of the batch is still running

	case harness.EvDone:
		m = m.endThinking()
		m.run.pending = nil
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.EndAnswer()
		m = m.saveTokens(true)
		m = m.saveState()

	case harness.EvError:
		m = m.endThinking()
		m.run.pending = nil
		m = m.saveTokens(true)
		msg := "request failed"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		if m.cfg != nil && m.cfg.Log.File != "" {
			msg += "  (log: " + m.cfg.Log.File + ")"
		}
		m.chat = m.chat.Fail(msg)
		m = m.saveState()
	}
	return m
}

// finishPending takes a returned call out of the running batch.
func (m Model) finishPending(c tools.Call) Model {
	label := callLabel(c)
	idx := -1
	for i, p := range m.run.pending {
		if (c.ID != "" && p.id == c.ID) || (c.ID == "" && p.label == label) {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i, p := range m.run.pending {
			if p.label == label {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return m
	}
	rest := make([]pendingCall, 0, len(m.run.pending)-1)
	rest = append(rest, m.run.pending[:idx]...)
	rest = append(rest, m.run.pending[idx+1:]...)
	m.run.pending = rest
	return m
}

// showPending draws the one transient line for calls that have not returned:
// the call itself when there is one, a short summary when there are several.
// It replaces whatever transient line was there, so the timeline never gains
// or loses rows except when a result arrives.
func (m Model) showPending() Model {
	m.chat = m.chat.EndWrite()
	switch n := len(m.run.pending); {
	case n == 0:
	case n == 1:
		c := m.run.pending[0].call
		switch c.Name {
		case "edit_file":
			path, _ := tools.StringArg(c.Args, "path")
			old, _ := tools.StringArg(c.Args, "old_string")
			body, _ := tools.StringArg(c.Args, "new_string")
			m.chat = m.chat.WriteProgress("edit_file", path, countLines(body), strings.TrimSpace(old) == "")
		case "create_file":
			path, _ := tools.StringArg(c.Args, "path")
			body, _ := tools.StringArg(c.Args, "content")
			m.chat = m.chat.WriteProgress("create_file", path, countLines(body), true)
		default:
			m.chat = m.chat.ShellStart(m.run.pending[0].label)
		}
	default:
		labels := make([]string, 0, len(m.run.pending))
		for _, p := range m.run.pending {
			labels = append(labels, p.label)
		}
		m.chat = m.chat.ShellStart(fmt.Sprintf("%d tools in parallel: %s", len(labels), strings.Join(labels, ", ")))
	}
	return m
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func (m Model) endThinking() Model {
	if m.run.thinkAt.IsZero() {
		// No thought is open. EndThinking only touches an unfinished one, so
		// this is harmless, and it must not invent a duration.
		m.chat = m.chat.EndThinking(time.Second)
		return m
	}
	m.chat = m.chat.EndThinking(time.Since(m.run.thinkAt))
	m.run.thinkAt = time.Time{} // the next thought is timed from its own start
	return m
}

// maxNodeLines bounds what one file node draws. A created file is shown in
// full up to this many lines, then a notice says how many were left out.
const maxNodeLines = 500

var firstLineRe = regexp.MustCompile(`first at line (\d+)`)

// shortError is the one line the person sees for a failed call.
func shortError(r *tools.Result) string {
	if strings.TrimSpace(r.Short) != "" {
		return r.Short
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(r.Output), "\n", 2)[0])
	if line == "" {
		return "the tool failed"
	}
	return line
}

// editDisplay builds what a file step shows. create_file, and edit_file with
// an empty old_string, create a file: every line of the content, numbered.
// An edit shows the removed lines and the added lines with their line numbers.
func editDisplay(c tools.Call, res *tools.Result) (path string, create bool, diff []components.DiffLine, ok bool) {
	path, _ = tools.StringArg(c.Args, "path")
	if strings.TrimSpace(path) == "" {
		return "", c.Name == "create_file", nil, false
	}
	var oldS, newS string
	if c.Name == "create_file" {
		create = true
		newS, _ = tools.StringArg(c.Args, "content")
	} else {
		oldS, _ = tools.StringArg(c.Args, "old_string")
		newS, _ = tools.StringArg(c.Args, "new_string")
		create = strings.TrimSpace(oldS) == ""
	}

	start := 1
	if !create && res != nil {
		if m := firstLineRe.FindStringSubmatch(res.Output); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				start = n
			}
		}
		for _, l := range textLines(oldS) {
			diff = append(diff, components.DiffLine{Kind: components.DiffDel, Num: start, Text: l})
			start++
		}
	}
	kind := components.DiffAdd
	if create {
		kind = components.DiffContext // a whole new file is plain numbered text, not a green wall
	}
	for _, l := range textLines(newS) {
		diff = append(diff, components.DiffLine{Kind: kind, Num: start, Text: l})
		start++
	}
	if len(diff) > maxNodeLines {
		more := len(diff) - maxNodeLines
		diff = append(diff[:maxNodeLines], components.DiffLine{Text: fmt.Sprintf("… %d more lines", more)})
	}
	return path, create, diff, true
}

func textLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

func callLabel(c tools.Call) string {
	switch c.Name {
	case "read_file":
		if p, _ := c.Args["path"].(string); p != "" {
			return clipLine("read " + p)
		}
	case "create_file":
		if p, _ := c.Args["path"].(string); p != "" {
			return clipLine("create " + p)
		}
	case "shell":
		if cmd, _ := c.Args["command"].(string); strings.TrimSpace(cmd) != "" {
			return clipLine(cmd)
		}
	case "edit_file":
		if p, _ := c.Args["path"].(string); p != "" {
			if old, _ := c.Args["old_string"].(string); old == "" {
				return clipLine("create " + p)
			}
			return clipLine("edit " + p)
		}
	}
	args, _ := json.Marshal(c.Args)
	s := c.Name
	if len(c.Args) > 0 {
		s += " " + string(args)
	}
	return clipLine(s)
}

func clipLine(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s))
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}
