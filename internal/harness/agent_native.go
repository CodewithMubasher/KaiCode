package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"kai/internal/tools"
)

// This file is the native path: OpenAI-compatible APIs. It does not import the
// dsml package and shares no prompt, parser or message format with the DSML
// path in agent_web.go:
//
//   - instructions travel as the API's system message, not inside a user message;
//   - tools are sent as structured definitions, not as a DSML block in a prompt;
//   - tool calls come back as structured calls (StreamToolCall), so the reply
//     text is shown as it is: nothing is parsed or stripped out of it;
//   - results go back as "tool" messages tied to the call's id, not as
//     <tool_result> text.
//
// The API is stateless, so the agent keeps the conversation (a.msgs) and sends
// all of it with every request.

const (
	defaultMaxContextBytes = 600 << 10 // about 150k tokens
	oldToolOutputMax       = 2 << 10   // old tool outputs are cut to this when the context is full
)

// systemNative is the system message of the native path.
func (a *Agent) systemNative() string {
	if s := strings.TrimSpace(a.cfg.NativeSystem); s != "" {
		return s
	}
	return strings.TrimSpace(a.cfg.System)
}

// beginNativeRun adds the user's message to the conversation.
func (a *Agent) beginNativeRun(user string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Reasoning is echoed back to the API only while the request that made it
	// is running (tool loops need it); a new user message drops all of it.
	for i := range a.msgs {
		a.msgs[i].Reasoning = ""
	}

	body := user
	if len(a.msgs) == 0 && a.recap != "" {
		body = "Recap of the conversation so far (the earlier messages are no longer available):\n\n" +
			a.recap + "\n\nContinue from here.\n\n" + user
	}
	a.recap = ""

	// A request that failed before any reply leaves its user message
	// unanswered; the new message joins it instead of making two in a row.
	if n := len(a.msgs); n > 0 && a.msgs[n-1].Role == "user" {
		a.msgs[n-1].Content += "\n\n" + body
		return
	}
	a.msgs = append(a.msgs, ChatMessage{Role: "user", Content: body})
}

// nativeRequest builds the next request: the system message, the conversation
// (trimmed to fit the budget) and the tool definitions.
func (a *Agent) nativeRequest() ChatRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.msgs = trimConversation(a.msgs, a.cfg.MaxContextBytes)
	msgs := make([]ChatMessage, 0, len(a.msgs)+1)
	if sys := a.systemNative(); sys != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: sys})
	}
	msgs = append(msgs, a.msgs...)
	return ChatRequest{Messages: msgs, Tools: a.cfg.Tools.Specs()}
}

func (a *Agent) setTokens(n int) {
	a.mu.Lock()
	a.tokens = n
	a.mu.Unlock()
}

func (a *Agent) appendMsg(m ChatMessage) {
	a.mu.Lock()
	a.msgs = append(a.msgs, m)
	a.mu.Unlock()
}

// appendAssistant stores the model's reply. An empty reply is not stored: the
// APIs reject an assistant message with neither text nor tool calls.
func (a *Agent) appendAssistant(text, reasoning string, calls []ToolCall) {
	if strings.TrimSpace(text) == "" && len(calls) == 0 {
		return
	}
	a.appendMsg(ChatMessage{Role: "assistant", Content: text, Reasoning: reasoning, ToolCalls: calls})
}

// closeOpenCalls makes sure every tool call in the conversation has a result.
// A stopped request can leave the last assistant message with calls nobody
// answered, and the API refuses a conversation like that.
func (a *Agent) closeOpenCalls() {
	a.mu.Lock()
	defer a.mu.Unlock()
	last := -1
	for i := len(a.msgs) - 1; i >= 0; i-- {
		if a.msgs[i].Role == "assistant" {
			last = i
			break
		}
	}
	if last < 0 || len(a.msgs[last].ToolCalls) == 0 {
		return
	}
	answered := map[string]bool{}
	for _, m := range a.msgs[last+1:] {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, c := range a.msgs[last].ToolCalls {
		if !answered[c.ID] {
			a.msgs = append(a.msgs, ChatMessage{Role: "tool", ToolCallID: c.ID,
				Content: "Not run: the request was stopped before this call finished."})
		}
	}
}

// loopNative handles one user request on the native path.
func (a *Agent) loopNative(ctx context.Context, user string, emit func(Event) bool) {
	a.record("user", user, "")
	a.beginNativeRun(user)
	defer a.closeOpenCalls()
	var guard failGuard

	for step := 0; step < a.cfg.MaxSteps; step++ {
		calls, err := a.nativeTurn(ctx, emit)
		if ctx.Err() != nil {
			return // stopped by the user
		}
		if err != nil {
			slog.Error("agent turn failed", "native", true, "step", step, "err", err)
			emit(Event{Kind: EvError, Err: err})
			return
		}
		if len(calls) == 0 {
			emit(Event{Kind: EvDone})
			return
		}

		// Announce every call first so the timeline shows the whole batch,
		// then run the batch in parallel. Results come back in call order.
		norm := make([]tools.Call, len(calls))
		perrs := make([]error, len(calls))
		for i, tc := range calls {
			var call tools.Call
			call, perrs[i] = toToolCall(tc)
			norm[i] = a.cfg.Tools.Normalize(call)
			if !emit(Event{Kind: EvToolCall, Call: &norm[i]}) {
				return
			}
		}
		stopped := false
		ok := runBatch(ctx, norm,
			func(i int, c tools.Call) tools.Result {
				if perrs[i] != nil {
					out := perrs[i].Error()
					if h := a.cfg.Tools.Help(c.Name); h != "" {
						out += "\n" + h
					}
					return tools.Result{ID: c.ID, Name: c.Name, IsError: true, Output: out, Short: perrs[i].Error()}
				}
				return a.cfg.Tools.Execute(ctx, c, a.cfg.ToolTimeout)
			},
			func(i int, r tools.Result) bool {
				stop := guard.note(a.cfg.Tools, norm[i], &r)
				if !emit(Event{Kind: EvToolResult, Call: &norm[i], Result: &r}) {
					return false
				}
				a.record("tool", r.Output, "")
				a.appendMsg(toolMessage(calls[i].ID, r))
				if stop {
					stopped = true
					emit(Event{Kind: EvError, Err: repeatError(norm[i].Name)})
					return false
				}
				return true
			})
		if !ok || stopped {
			return
		}
	}
	emit(Event{Kind: EvError, Err: fmt.Errorf("stopped after %d steps without a final answer", a.cfg.MaxSteps)})
}

// nativeTurn sends the conversation and reads the whole reply, forwarding
// text and thinking as they arrive. It returns the tool calls the model made.
func (a *Agent) nativeTurn(ctx context.Context, emit func(Event) bool) (calls []ToolCall, err error) {
	req := a.nativeRequest()
	used := EstimateRequestTokens(req)
	a.setTokens(used)
	slog.Info("agent turn", "native", true, "messages", len(req.Messages), "tools", len(req.Tools), "tokens", used)

	stream, err := a.nativeProvider().Chat(ctx, req)
	if err != nil {
		return nil, err
	}

	shown := -1
	exact := false
	usage := func(force bool) {
		tok := a.tokensNow()
		if tok == shown || (!force && tok-shown < 40) {
			return
		}
		shown = tok
		emit(Event{Kind: EvUsage, Tokens: tok, Exact: exact})
	}
	usage(true)

	var text, reasoning strings.Builder
	var gate progressGate
	ended, committed := false, false

	// Whatever happens, the conversation keeps what the user saw, including a
	// reply that was stopped half way. Calls that never completed are dropped.
	defer func() {
		if !committed && text.Len() > 0 {
			a.appendAssistant(text.String(), "", nil)
		}
	}()

read:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-stream:
			if !ok {
				break read
			}
			switch ev.Kind {
			case StreamThinking:
				reasoning.WriteString(ev.Text)
				if !exact {
					a.addTokens(EstimateTokens(ev.Text))
				}
				usage(false)
				if !emit(Event{Kind: EvThinking, Text: ev.Text}) {
					return nil, ctx.Err()
				}
			case StreamText:
				text.WriteString(ev.Text)
				if !exact {
					a.addTokens(EstimateTokens(ev.Text))
				}
				usage(false)
				if !emit(Event{Kind: EvText, Text: ev.Text}) {
					return nil, ctx.Err()
				}
			case StreamUsage:
				if ev.Usage != nil {
					if n := ev.Usage.Total(); n > 0 {
						exact = true
						a.setTokens(n)
						usage(true)
					}
				}
			case StreamToolProgress:
				if ev.Tool != nil {
					hp := *ev.Tool
					if gate.offer(&hp) && !emit(Event{Kind: EvToolProgress, Progress: &hp}) {
						return nil, ctx.Err()
					}
				}
			case StreamToolCall:
				if ev.ToolCall != nil {
					calls = append(calls, *ev.ToolCall)
					if !exact {
						a.addTokens(EstimateTokens(ev.ToolCall.Name) + EstimateTokens(ev.ToolCall.Arguments))
					}
				}
			case StreamEnd:
				ended = true
				if st := ev.Text; st != "" && st != "FINISHED" {
					return nil, fmt.Errorf("the reply ended early (status %s)", st)
				}
			case StreamError:
				if !ended {
					if ev.Err == nil {
						return nil, errors.New("the stream failed")
					}
					return nil, ev.Err
				}
			}
		}
	}
	usage(true)
	if hp := gate.pending(); hp != nil {
		emit(Event{Kind: EvToolProgress, Progress: hp})
	}
	if !ended {
		return nil, errors.New("the reply stream closed before the reply was finished")
	}

	a.appendAssistant(text.String(), reasoning.String(), calls)
	committed = true
	if text.Len() > 0 {
		a.record("assistant", text.String(), "")
	}
	return calls, nil
}

// toToolCall reads a native call. A call whose arguments are not a JSON object
// comes back with an error that goes to the model as the call's result.
func toToolCall(tc ToolCall) (tools.Call, error) {
	args, err := parseToolArgs(tc.Arguments)
	return tools.Call{ID: tc.ID, Name: tc.Name, Args: args}, err
}

// parseToolArgs decodes the JSON text of a call's arguments. An empty string
// means no arguments. Some models encode the object twice (a JSON string that
// holds JSON); that is accepted too.
func parseToolArgs(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}, nil
	}
	bad := func(err error) error {
		return fmt.Errorf("the arguments are not valid JSON (%v); send the call again with the arguments as one JSON object", err)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, bad(err)
	}
	if s, ok := v.(string); ok {
		v = nil
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, bad(err)
		}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("the arguments must be a JSON object; send the call again")
	}
	return m, nil
}

// toolMessage is a call's result as the API wants it: a "tool" message tied to
// the call's id.
func toolMessage(id string, r tools.Result) ChatMessage {
	out := r.Output
	if r.IsError {
		out = "Error: " + out
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)" // some APIs refuse an empty tool message
	}
	return ChatMessage{Role: "tool", ToolCallID: id, Content: out}
}

// ---- size ------------------------------------------------------------------

func messageSize(m ChatMessage) int {
	n := len(m.Content) + len(m.Reasoning)
	for _, c := range m.ToolCalls {
		n += len(c.Name) + len(c.Arguments)
	}
	return n
}

func conversationSize(msgs []ChatMessage) int {
	n := 0
	for _, m := range msgs {
		n += messageSize(m)
	}
	return n
}

// EstimateRequestTokens approximates what a request costs: every message and
// every tool definition, since the API reads all of it again each time.
func EstimateRequestTokens(req ChatRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += EstimateTokens(m.Content) + EstimateTokens(m.Reasoning)
		for _, c := range m.ToolCalls {
			n += EstimateTokens(c.Name) + EstimateTokens(c.Arguments)
		}
	}
	for _, s := range req.Tools {
		n += EstimateTokens(s.JSON())
	}
	return n
}

// trimConversation makes the conversation fit the budget without breaking it:
// an assistant message with tool calls always keeps the tool messages that
// answer it. First old tool outputs are cut short, then the oldest exchanges
// (a user message and everything up to the next one) are dropped, then the
// outputs inside the current exchange, except the newest results. The newest
// exchange is never dropped.
func trimConversation(msgs []ChatMessage, budget int) []ChatMessage {
	if conversationSize(msgs) <= budget {
		return msgs
	}
	out := append([]ChatMessage(nil), msgs...)
	size := conversationSize(out)

	shrink := func(before int) {
		for i := 0; i < before && i < len(out); i++ {
			if out[i].Role == "tool" && len(out[i].Content) > oldToolOutputMax {
				size -= len(out[i].Content)
				out[i].Content = tools.Truncate(out[i].Content, oldToolOutputMax)
				size += len(out[i].Content)
			}
		}
	}
	lastOf := func(role string) int {
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].Role == role {
				return i
			}
		}
		return -1
	}

	shrink(lastOf("user"))
	for size > budget {
		next := -1
		for i := 1; i < len(out); i++ {
			if out[i].Role == "user" {
				next = i
				break
			}
		}
		if next < 0 {
			break // one exchange left
		}
		for _, m := range out[:next] {
			size -= messageSize(m)
		}
		out = out[next:]
	}
	if size > budget {
		shrink(lastOf("assistant"))
	}
	return out
}
