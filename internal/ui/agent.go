package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"kai/internal/bridge"
	"kai/internal/config"
	"kai/internal/harness"
	"kai/internal/tools"
)

// kaiSystem is the instruction block sent with the first message of a chat.
const kaiSystem = "You are Kai, a coding agent working in the user's project directory. " +
	"Use the tools to look at files before answering questions about them. Keep answers short and concrete."

// runState is the agent and the request currently in flight.
type runState struct {
	agent   *harness.Agent
	events  <-chan harness.Event
	cancel  context.CancelFunc
	id      int       // bumps on every start and stop, so late events can be ignored
	thinkAt time.Time // when the model started thinking
}

// agentMsg carries one harness event to Update. agentDoneMsg means the
// harness closed its channel.
type (
	agentMsg     struct {
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

	var provider harness.Provider
	if cfg.Provider.Name == "mock" {
		provider = &harness.MockProvider{Script: harness.DemoScript, ChunkRunes: 4, Delay: 15 * time.Millisecond}
	} else {
		provider = startBridge(cfg)
	}

	return runState{agent: harness.New(harness.Config{
		Provider: provider,
		Tools:    reg,
		System:   kaiSystem,
		Record:   sessionLogger(cfg.Session.Dir),
	})}
}

// startBridge opens the local port the Chrome extension connects to. If that
// fails, every send reports why instead of the app refusing to start.
func startBridge(cfg *config.Config) harness.Provider {
	addr := cfg.Provider.Addr
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	token, err := bridge.LoadOrCreateToken(config.KaiDir())
	if err != nil {
		return brokenProvider{fmt.Errorf("bridge token: %w", err)}
	}
	srv, err := bridge.Start(addr, token, slog.Default())
	if err != nil {
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
		m.chat = m.chat.StreamThinking(ev.Text)

	case harness.EvText:
		m = m.endThinking()
		m.chat = m.chat.StreamAnswer(ev.Text)

	case harness.EvToolCall:
		m = m.endThinking()
		m.chat = m.chat.DemoteAnswer() // text before a tool call was narration
		if ev.Call != nil && ev.Call.Name == "read_file" {
			path, _ := ev.Call.Args["path"].(string)
			m.chat = m.chat.ReadFile(path)
		}

	case harness.EvToolResult:
		if ev.Call == nil || ev.Result == nil {
			break
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
		m.chat = m.chat.EndAnswer()

	case harness.EvError:
		msg := "request failed"
		if ev.Err != nil {
			msg = ev.Err.Error()
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

// callLabel is a one-line "name {args}" for a tool call.
func callLabel(c tools.Call) string {
	args, _ := json.Marshal(c.Args)
	s := c.Name
	if len(c.Args) > 0 {
		s += " " + string(args)
	}
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}
