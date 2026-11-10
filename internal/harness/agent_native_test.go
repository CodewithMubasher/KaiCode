package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"kai/internal/tools"
)

// fakeNative is a scripted NativeProvider. It records every request.
type fakeNative struct {
	mu     sync.Mutex
	reqs   []ChatRequest
	script func(n int, req ChatRequest) []StreamEvent
	err    error
}

func (f *fakeNative) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	evs := f.script(n, req)
	ch := make(chan StreamEvent, len(evs)+1)
	for _, e := range evs {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (f *fakeNative) request(i int) ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[i]
}

func text(s string) StreamEvent  { return StreamEvent{Kind: StreamText, Text: s} }
func think(s string) StreamEvent { return StreamEvent{Kind: StreamThinking, Text: s} }
func end() StreamEvent           { return StreamEvent{Kind: StreamEnd, Text: "FINISHED"} }
func callEv(id, name, args string) StreamEvent {
	return StreamEvent{Kind: StreamToolCall, ToolCall: &ToolCall{ID: id, Name: name, Arguments: args}}
}

func newNativeAgent(f *fakeNative) *Agent {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	return New(Config{Native: f, Tools: reg, System: "web system", NativeSystem: "native system", ToolTimeout: 5 * time.Second})
}

func noDSML(t *testing.T, req ChatRequest) {
	t.Helper()
	for _, m := range req.Messages {
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), "DSML") || strings.Contains(string(raw), "kai_instructions") || strings.Contains(string(raw), "<tool_result>") {
			t.Fatalf("DSML text leaked into a native request: %s", raw)
		}
	}
}

func TestNativeToolLoop(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		if n == 0 {
			return []StreamEvent{text("Looking."), callEv("c1", "echo", `{"text":"hi"}`), end()}
		}
		return []StreamEvent{text("All done."), end()}
	}}
	a := newNativeAgent(f)
	evs := drain(t, a.Run(context.Background(), "go"))

	want := []EventKind{EvText, EvToolCall, EvToolResult, EvText, EvDone}
	got := kinds(evs)
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
	if textOf(evs) != "Looking.All done." {
		t.Fatalf("text %q", textOf(evs))
	}

	r0 := f.request(0)
	if r0.Messages[0].Role != "system" || r0.Messages[0].Content != "native system" {
		t.Fatalf("the native path must use NativeSystem as the system message: %+v", r0.Messages[0])
	}
	if r0.Messages[1].Role != "user" || r0.Messages[1].Content != "go" {
		t.Fatalf("the user message must be sent as typed, with nothing wrapped around it: %+v", r0.Messages[1])
	}
	if len(r0.Tools) != 1 || r0.Tools[0].Name != "echo" {
		t.Fatalf("tools: %+v", r0.Tools)
	}

	r1 := f.request(1)
	if len(r1.Messages) != 4 {
		t.Fatalf("want system, user, assistant, tool; got %+v", r1.Messages)
	}
	as, tm := r1.Messages[2], r1.Messages[3]
	if as.Role != "assistant" || as.Content != "Looking." || len(as.ToolCalls) != 1 || as.ToolCalls[0].ID != "c1" {
		t.Fatalf("assistant message: %+v", as)
	}
	if tm.Role != "tool" || tm.ToolCallID != "c1" || tm.Content != "echo:hi" {
		t.Fatalf("results go back as tool messages tied to the call id: %+v", tm)
	}
	noDSML(t, r0)
	noDSML(t, r1)
}

func TestNativeReplyWithDSMLTextIsNotParsed(t *testing.T) {
	const markup = `<|DSML|tool_calls><|DSML|invoke name="echo"><|DSML|parameter name="text" string="true">x</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		return []StreamEvent{text(markup), end()}
	}}
	a := newNativeAgent(f)
	evs := drain(t, a.Run(context.Background(), "go"))
	for _, e := range evs {
		if e.Kind == EvToolCall {
			t.Fatal("DSML in a native reply must not be run as a tool call")
		}
	}
	if textOf(evs) != markup {
		t.Fatalf("native text is shown exactly as it came, got %q", textOf(evs))
	}
	if len(f.reqs) != 1 {
		t.Fatalf("no repair round-trip on the native path, got %d requests", len(f.reqs))
	}
}

func TestNativeBadArgumentsAreReportedToTheModel(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		if n == 0 {
			return []StreamEvent{callEv("c1", "echo", `{"text": oops`), end()}
		}
		return []StreamEvent{text("sorry"), end()}
	}}
	a := newNativeAgent(f)
	evs := drain(t, a.Run(context.Background(), "go"))

	var res *tools.Result
	for _, e := range evs {
		if e.Kind == EvToolResult {
			res = e.Result
		}
	}
	if res == nil || !res.IsError || !strings.Contains(res.Output, "not valid JSON") {
		t.Fatalf("result: %+v", res)
	}
	tm := f.request(1).Messages[3]
	if tm.Role != "tool" || tm.ToolCallID != "c1" || !strings.HasPrefix(tm.Content, "Error: ") {
		t.Fatalf("every call needs a tool message, errors included: %+v", tm)
	}
}

func TestNativeReasoningStaysOnlyInsideARequest(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		switch n {
		case 0:
			return []StreamEvent{think("plan"), callEv("c1", "echo", `{"text":"a"}`), end()}
		case 1:
			return []StreamEvent{think("more"), text("first answer"), end()}
		}
		return []StreamEvent{text("second answer"), end()}
	}}
	a := newNativeAgent(f)
	drain(t, a.Run(context.Background(), "one"))
	drain(t, a.Run(context.Background(), "two"))

	// Inside the first request the tool loop needs its reasoning back.
	if got := f.request(1).Messages[2].Reasoning; got != "plan" {
		t.Fatalf("reasoning must come back during a tool loop, got %q", got)
	}
	// A new user message drops all reasoning.
	for _, m := range f.request(2).Messages {
		if m.Reasoning != "" {
			t.Fatalf("reasoning of an earlier request must not be sent: %+v", m)
		}
	}
	// And the conversation is intact.
	var roles []string
	for _, m := range f.request(2).Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,assistant,user" {
		t.Fatalf("roles: %v", roles)
	}
}

func TestNativeStopLeavesNoUnansweredCalls(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		return []StreamEvent{callEv("c1", "block", `{}`), callEv("c2", "echo", `{"text":"x"}`), end()}
	}}
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	reg.Register(blockTool{})
	a := New(Config{Native: f, Tools: reg})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := a.Run(ctx, "go")
	for e := range ch {
		if e.Kind == EvToolCall {
			cancel() // like pressing esc while a tool runs
		}
	}

	a.mu.Lock()
	msgs := append([]ChatMessage(nil), a.msgs...)
	a.mu.Unlock()
	answered := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	if !answered["c1"] || !answered["c2"] {
		t.Fatalf("every call needs a result or the next request is refused: %+v", msgs)
	}

	// The conversation can go on.
	f.script = func(n int, req ChatRequest) []StreamEvent { return []StreamEvent{text("ok"), end()} }
	drain(t, a.Run(context.Background(), "again"))
}

type blockTool struct{}

func (blockTool) Spec() tools.Spec { return tools.Spec{Name: "block", Description: "Waits."} }
func (blockTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestNativeFailedRequestDoesNotBreakTheNextOne(t *testing.T) {
	f := &fakeNative{err: errors.New("503 overloaded"), script: func(n int, req ChatRequest) []StreamEvent {
		return []StreamEvent{text("ok"), end()}
	}}
	a := newNativeAgent(f)
	evs := drain(t, a.Run(context.Background(), "first"))
	if last := evs[len(evs)-1]; last.Kind != EvError || !strings.Contains(last.Err.Error(), "503") {
		t.Fatalf("want the provider's error, got %+v", last)
	}

	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	drain(t, a.Run(context.Background(), "second"))
	msgs := f.request(1).Messages
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "first") || !strings.Contains(msgs[1].Content, "second") {
		t.Fatalf("two user messages in a row must be merged into one: %+v", msgs)
	}
}

func TestNativeStreamErrorKeepsPartialText(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		if n == 0 {
			return []StreamEvent{text("half an ans"), {Kind: StreamError, Err: errors.New("connection reset")}}
		}
		return []StreamEvent{text("ok"), end()}
	}}
	a := newNativeAgent(f)
	evs := drain(t, a.Run(context.Background(), "go"))
	if evs[len(evs)-1].Kind != EvError {
		t.Fatalf("events: %v", kinds(evs))
	}
	drain(t, a.Run(context.Background(), "continue"))
	msgs := f.request(1).Messages
	if len(msgs) != 4 || msgs[2].Role != "assistant" || msgs[2].Content != "half an ans" {
		t.Fatalf("the user saw the partial reply, so the model must remember it: %+v", msgs)
	}
}

func TestNativeLengthStatusIsAnError(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		return []StreamEvent{text("cut"), {Kind: StreamEnd, Text: "length"}}
	}}
	evs := drain(t, newNativeAgent(f).Run(context.Background(), "go"))
	if e := evs[len(evs)-1]; e.Kind != EvError || !strings.Contains(e.Err.Error(), "length") {
		t.Fatalf("got %+v", e)
	}
}

func TestNativeProgressAndUsageEvents(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent {
		return []StreamEvent{
			{Kind: StreamToolProgress, Tool: &ToolProgress{Name: "edit_file", Path: "a.go", Lines: 1, Creating: true}},
			{Kind: StreamToolProgress, Tool: &ToolProgress{Name: "edit_file", Path: "a.go", Lines: 50, Creating: true}},
			text("done"), end(),
		}
	}}
	evs := drain(t, newNativeAgent(f).Run(context.Background(), "go"))
	var prog []int
	tokens := 0
	for _, e := range evs {
		switch e.Kind {
		case EvToolProgress:
			prog = append(prog, e.Progress.Lines)
		case EvUsage:
			tokens = e.Tokens
		}
	}
	if len(prog) != 2 || prog[0] != 1 || prog[1] != 50 {
		t.Fatalf("progress lines %v", prog)
	}
	if tokens == 0 {
		t.Fatal("the context gauge needs a usage event")
	}
}

func TestNativeResumeSendsRecapInTheUserMessage(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent { return []StreamEvent{text("ok"), end()} }}
	a := newNativeAgent(f)
	a.Resume([]Entry{{Role: "user", Text: "build a parser"}, {Role: "assistant", Text: "the parser is built"}})
	drain(t, a.Run(context.Background(), "now add tests"))

	msgs := f.request(0).Messages
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[0].Content != "native system" {
		t.Fatalf("messages: %+v", msgs)
	}
	for _, want := range []string{"build a parser", "the parser is built", "now add tests"} {
		if !strings.Contains(msgs[1].Content, want) {
			t.Errorf("the recap and the new message must both be in the first message, missing %q:\n%s", want, msgs[1].Content)
		}
	}
	noDSML(t, f.request(0))
}

func TestNativeSystemFallsBackToSystem(t *testing.T) {
	f := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent { return []StreamEvent{text("ok"), end()} }}
	a := New(Config{Native: f, System: "shared system"})
	drain(t, a.Run(context.Background(), "go"))
	if got := f.request(0).Messages[0].Content; got != "shared system" {
		t.Fatalf("got %q", got)
	}
}

// The two paths are chosen by the backend and never mix: switching back and
// forth keeps the conversation but not the wire formats.
func TestSwitchingBetweenWebAndNativeKeepsThePathsApart(t *testing.T) {
	var webPrompt string
	web := &MockProvider{ChunkRunes: 8, Script: func(n int, tt Turn) string { webPrompt = tt.Prompt; return "web answer" }}
	nat := &fakeNative{script: func(n int, req ChatRequest) []StreamEvent { return []StreamEvent{text("native answer"), end()} }}

	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	a := New(Config{Provider: web, Tools: reg, System: "web system", NativeSystem: "native system"})
	drain(t, a.Run(context.Background(), "one"))
	if !strings.Contains(webPrompt, "<|DSML|tool_calls>") || !strings.Contains(webPrompt, "web system") || strings.Contains(webPrompt, "native system") {
		t.Fatalf("the web path sends DSML instructions in its first prompt:\n%s", webPrompt)
	}

	a.SetNative(nat)
	drain(t, a.Run(context.Background(), "two"))
	r := nat.request(0)
	noDSML(t, r)
	if r.Messages[0].Content != "native system" || len(r.Tools) != 1 {
		t.Fatalf("native request: %+v", r)
	}
	if u := r.Messages[1].Content; !strings.Contains(u, "one") || !strings.Contains(u, "web answer") || !strings.Contains(u, "two") {
		t.Fatalf("the conversation must carry over as a recap: %q", u)
	}

	a.SetProvider(web)
	drain(t, a.Run(context.Background(), "three"))
	if !strings.Contains(webPrompt, "<|DSML|tool_calls>") || strings.Contains(webPrompt, "native system") {
		t.Fatalf("back on the web path:\n%s", webPrompt)
	}
	if a.nativeProvider() != nil {
		t.Fatal("SetProvider must clear the native provider")
	}
}

func TestParseToolArgs(t *testing.T) {
	ok := map[string]string{
		``:                   `{}`,
		`null`:               `{}`,
		`{}`:                 `{}`,
		`{"a":"b","n":2}`:    `{"a":"b","n":2}`,
		`"{\"a\":\"b\"}"`:    `{"a":"b"}`, // encoded twice
		"  {\"a\" : true}\n": `{"a":true}`,
	}
	for in, want := range ok {
		m, err := parseToolArgs(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		b, _ := json.Marshal(m)
		if string(b) != want {
			t.Errorf("%q -> %s, want %s", in, b, want)
		}
	}
	for _, bad := range []string{`{"a":`, `[1,2]`, `42`, `"not json"`, `{a:1}`} {
		if _, err := parseToolArgs(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// ---- trimming --------------------------------------------------------------

func exchange(user string, outputs ...int) []ChatMessage {
	msgs := []ChatMessage{{Role: "user", Content: user}}
	for i, n := range outputs {
		id := user + string(rune('a'+i))
		msgs = append(msgs,
			ChatMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "echo", Arguments: "{}"}}},
			ChatMessage{Role: "tool", ToolCallID: id, Content: strings.Repeat("x", n)})
	}
	return append(msgs, ChatMessage{Role: "assistant", Content: "answer to " + user})
}

// valid reports whether every assistant message with calls is followed by
// exactly its results, and no tool message is orphaned.
func valid(msgs []ChatMessage) bool {
	for i := 0; i < len(msgs); i++ {
		switch msgs[i].Role {
		case "tool":
			if i == 0 || (msgs[i-1].Role != "tool" && len(msgs[i-1].ToolCalls) == 0) {
				return false
			}
		case "assistant":
			for j, c := range msgs[i].ToolCalls {
				if i+1+j >= len(msgs) || msgs[i+1+j].Role != "tool" || msgs[i+1+j].ToolCallID != c.ID {
					return false
				}
			}
		}
	}
	return true
}

func TestTrimConversation(t *testing.T) {
	var msgs []ChatMessage
	msgs = append(msgs, exchange("one", 5000, 5000)...)
	msgs = append(msgs, exchange("two", 5000)...)
	msgs = append(msgs, exchange("three", 5000)...)
	total := conversationSize(msgs)

	if got := trimConversation(msgs, total); len(got) != len(msgs) {
		t.Fatal("a conversation that fits is left alone")
	}

	// Shrinking old tool outputs is enough: nothing is dropped.
	got := trimConversation(msgs, total-4000)
	if len(got) != len(msgs) || !valid(got) {
		t.Fatalf("old outputs should be shortened first, nothing dropped: %d of %d valid=%v", len(got), len(msgs), valid(got))
	}
	if conversationSize(got) > total-4000 {
		t.Fatalf("still too big: %d", conversationSize(got))
	}
	if len(msgs[2].Content) != 5000 {
		t.Fatal("trimming must not change the caller's slice")
	}

	// Then the oldest exchanges go, whole.
	got = trimConversation(msgs, 6000)
	if !valid(got) || got[0].Role != "user" || got[0].Content == "one" {
		t.Fatalf("must start at an exchange boundary and stay valid: %+v", got[0])
	}
	if last := got[len(got)-1]; last.Content != "answer to three" {
		t.Fatalf("the newest exchange is never dropped: %+v", last)
	}

	// A single huge exchange is kept (the API may refuse it, but we do not corrupt it).
	one := exchange("big", 100000, 100000)
	got = trimConversation(one, 1000)
	if !valid(got) || got[0].Content != "big" {
		t.Fatalf("a single exchange stays whole: %+v", got)
	}
}
