package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"kai/internal/harness/dsml"
	"kai/internal/tools"
)

// This file is the DSML path: DeepSeek Web, a text-only chat. It is used only
// when the agent's backend is a Provider. Nothing here runs for a native
// (OpenAI-compatible) provider; that path lives in agent_native.go.

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
	a.tokens = 0 // the new chat starts empty
	a.session = newSessionID()
	a.recap = recap(a.history, recapBudget)
	slog.Warn("web chat lost: starting a new chat with a recap", "session", a.session)
}

func (a *Agent) loopWeb(ctx context.Context, user string, emit func(Event) bool) {
	a.record("user", user, "")
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
			a.record("assistant", res.text, "")
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
			a.record("tool", r.Output, "")
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
	stream, err := a.provider().Send(ctx, Turn{
		Prompt:   prompt,
		ParentID: parent,
		Thinking: a.cfg.Thinking,
		Session:  session,
		NewChat:  newChat,
	})
	if err != nil {
		return res, err
	}

	// The usage gauge follows what the provider actually sees and writes: the
	// prompt (instructions and tool specs included, on the first turn) plus
	// every delta the model streams, markup and all.
	used := -1
	usage := func(force bool) {
		tok := a.tokensNow()
		if tok == used || (!force && tok-used < 40) {
			return
		}
		used = tok
		emit(Event{Kind: EvUsage, Tokens: tok})
	}
	a.addTokens(EstimateTokens(prompt))
	usage(true)

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
	var gate progressGate

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
				a.addTokens(EstimateTokens(ev.Text))
				usage(false)
				if !emit(Event{Kind: EvThinking, Text: ev.Text}) {
					return res, ctx.Err()
				}
			case StreamText:
				visible := p.Feed(ev.Text)
				a.addTokens(EstimateTokens(ev.Text))
				usage(false)
				if tp, ok := p.Progress(); ok && tp.Name == "edit_file" {
					hp := &ToolProgress{Name: tp.Name, Path: tp.Path, Lines: tp.Lines, Creating: tp.Creating}
					if gate.offer(hp) {
						if !emit(Event{Kind: EvToolProgress, Progress: hp}) {
							return res, ctx.Err()
						}
					}
				}
				if !show(visible) {
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
	usage(true)
	// The count the throttle held back is the final one: show it.
	if hp := gate.pending(); hp != nil {
		emit(Event{Kind: EvToolProgress, Progress: hp})
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
