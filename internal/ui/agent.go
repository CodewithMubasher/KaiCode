package ui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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

// newRunState builds the agent.
//
// Until the Chrome bridge exists (Phase 5) there is no live DeepSeek session
// to talk to, so every provider name runs the scripted demo provider. The
// loop, the DSML parsing and the tools are the real ones.
func newRunState(cfg *config.Config) runState {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	provider := &harness.MockProvider{Script: harness.DemoScript, ChunkRunes: 4, Delay: 15 * time.Millisecond}
	return runState{agent: harness.New(harness.Config{
		Provider: provider,
		Tools:    reg,
		System:   kaiSystem,
	})}
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
