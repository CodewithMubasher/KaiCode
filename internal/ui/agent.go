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

// kaiSystem is the instruction block sent with the first message of a chat.
func kaiSystem() string {
	return "You are Kai, a coding agent working in the user's project directory.\n\n" + tools.SystemGuide()
}

// toolTimeout is how long one tool call may run. The shell tool enforces its
// own, shorter default (60 s) and accepts up to 10 minutes per call.
const toolTimeout = 11 * time.Minute

// runState is the agent and the request currently in flight.
type runState struct {
	agent   *harness.Agent
	events  <-chan harness.Event
	cancel  context.CancelFunc
	id      int       // bumps on every start and stop, so late events can be ignored
	thinkAt time.Time // when the model started thinking
	tokens  int       // what the provider's context holds (from EvUsage)
}

// agentMsg carries one harness event to Update. agentDoneMsg means the
// harness closed its channel.
type (
	agentMsg struct {
		id int
		ev harness.Event
	}
	agentDoneMsg struct{ id int }
)

// newRunState builds the agent. Provider "mock" runs a scripted demo that
// needs no browser; anything else starts the bridge for the Chrome extension.
func newRunState(cfg *config.Config) runState {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.Shell{})
	reg.Register(tools.EditFile{})

	var provider harness.Provider
	if cfg.Provider.Name == "mock" {
		provider = &harness.MockProvider{Script: harness.DemoScript, ChunkRunes: 4, Delay: 15 * time.Millisecond}
	} else {
		provider = startBridge(cfg)
	}

	return runState{agent: harness.New(harness.Config{
		Provider:    provider,
		Tools:       reg,
		System:      kaiSystem(),
		ToolTimeout: toolTimeout,
		Record:      sessionLogger(cfg.Session.Dir),
	})}
}

// startBridge opens the local port the Chrome extension connects to. There is
// nothing to configure on the extension side. If this fails, every send
// reports why instead of the app refusing to start.
func startBridge(cfg *config.Config) harness.Provider {
	addr := cfg.Provider.Addr
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	srv, err := bridge.Start(addr, slog.Default())
	if err != nil {
		slog.Error("could not start the bridge", "addr", addr, "err", err)
		return brokenProvider{fmt.Errorf("could not start the bridge on %s (is another Kai running?): %w", addr, err)}
	}
	return srv
}

type brokenProvider struct{ err error }

func (b brokenProvider) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	return nil, b.err
}

// sessionLogger appends every history entry to a JSON-lines file in dir, one
// file per Kai run. This is Kai's own record of the conversation, kept on
// disk next to the provider's copy.
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

// startRun sends text to the agent and starts listening for its events.
func (m Model) startRun(text string) (Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.run.id++
	m.run.cancel = cancel
	m.run.events = m.agent().Run(ctx, text)
	m.run.thinkAt = time.Time{}
	return m, waitEvent(m.run.id, m.run.events)
}

func (m Model) agent() *harness.Agent { return m.run.agent }

// stopRun cancels the request in flight, if any. Events still on their way
// are dropped because their id no longer matches.
func (m Model) stopRun() Model {
	if m.run.cancel != nil {
		m.run.cancel()
		m.run.cancel = nil
	}
	m.run.id++
	m.run.events = nil
	return m
}

// waitEvent blocks (off the UI loop) until the next harness event.
func waitEvent(id int, ch <-chan harness.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return agentDoneMsg{id}
		}
		return agentMsg{id, ev}
	}
}

// applyEvent shows one harness event in the chat.
func (m Model) applyEvent(ev harness.Event) Model {
	switch ev.Kind {
	case harness.EvThinking:
		if m.run.thinkAt.IsZero() {
			m.run.thinkAt = time.Now()
		}
		m.chat = m.chat.EndWrite() // reasoning after a call block: the call is done
		m.chat = m.chat.StreamThinking(ev.Text)

	case harness.EvText:
		m = m.endThinking()
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.StreamAnswer(ev.Text)

	case harness.EvUsage:
		m.run.tokens = ev.Tokens

	case harness.EvToolProgress:
		if ev.Progress != nil {
			m = m.endThinking()
			m.chat = m.chat.WriteProgress(ev.Progress.Name, ev.Progress.Path, ev.Progress.Lines, ev.Progress.Creating)
		}

	case harness.EvToolCall:
		m = m.endThinking()
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.DemoteAnswer() // text before a tool call was narration
		if ev.Call != nil && ev.Call.Name == "read_file" {
			path, _ := ev.Call.Args["path"].(string)
			m.chat = m.chat.ReadFile(path)
		}

	case harness.EvToolResult:
		if ev.Call == nil || ev.Result == nil {
			break
		}
		if ev.Call.Name == "edit_file" && !ev.Result.IsError {
			if path, create, diff, ok := editDisplay(*ev.Call, ev.Result); ok {
				m.chat = m.chat.EditFile(path, create, diff)
				break
			}
		}
		// A successful read_file is already on screen from its call.
		if ev.Call.Name != "read_file" || ev.Result.IsError {
			out := ev.Result.Output
			if ev.Result.IsError {
				out = "Error: " + out
			}
			m.chat = m.chat.RunShell(callLabel(*ev.Call), out)
		}

	case harness.EvDone:
		m = m.endThinking()
		m.chat = m.chat.EndWrite()
		m.chat = m.chat.EndAnswer()

	case harness.EvError:
		msg := "request failed"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		if m.cfg != nil && m.cfg.Log.File != "" {
			msg += "  (log: " + m.cfg.Log.File + ")"
		}
		m.chat = m.chat.Fail(msg)
	}
	return m
}

// endThinking closes the thinking step with the time it took.
func (m Model) endThinking() Model {
	took := time.Second
	if !m.run.thinkAt.IsZero() {
		took = time.Since(m.run.thinkAt)
	}
	m.chat = m.chat.EndThinking(took)
	return m
}

// maxDiffLines caps an edit card, so writing a large file cannot flood the
// chat. The rest is summarised in one row.
const maxDiffLines = 60

var firstLineRe = regexp.MustCompile(`first at line (\d+)`)

// editDisplay turns an edit_file call into the diff card the chat shows. The
// call arguments are the full text the model wrote, so creating a file shows
// every added line and an edit shows the removed and added blocks.
func editDisplay(c tools.Call, res *tools.Result) (path string, create bool, diff []components.DiffLine, ok bool) {
	path, _ = tools.StringArg(c.Args, "path")
	if strings.TrimSpace(path) == "" {
		return "", false, nil, false
	}
	oldS, _ := tools.StringArg(c.Args, "old_string")
	newS, _ := tools.StringArg(c.Args, "new_string")
	create = strings.TrimSpace(oldS) == ""

	start := 1
	if !create {
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
	for _, l := range textLines(newS) {
		diff = append(diff, components.DiffLine{Kind: components.DiffAdd, Num: start, Text: l})
		start++
	}
	if len(diff) > maxDiffLines {
		more := len(diff) - maxDiffLines
		diff = append(diff[:maxDiffLines], components.DiffLine{Text: fmt.Sprintf("… %d more lines", more)})
	}
	return path, create, diff, true
}

// textLines splits text into lines, ignoring the trailing newline.
func textLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

// callLabel is a one-line description of a tool call for the screen: the
// command for shell, "edit <path>" for edit_file, "name {args}" otherwise.
func callLabel(c tools.Call) string {
	switch c.Name {
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

// clipLine puts s on one line and cuts it to 100 characters.
func clipLine(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s))
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}
