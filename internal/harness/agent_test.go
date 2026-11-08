package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"kai/internal/tools"
)

type echoTool struct{}

func (echoTool) Spec() tools.Spec {
	return tools.Spec{Name: "echo", Description: "Echo text.", Params: []tools.Param{{Name: "text", Required: true}}}
}
func (echoTool) Run(ctx context.Context, args map[string]any) (string, error) {
	return "echo:" + args["text"].(string), nil
}

func call(name, text string) string {
	return "<|DSML|tool_calls><|DSML|invoke name=\"" + name + "\">" +
		"<|DSML|parameter name=\"text\" string=\"true\">" + text + "</|DSML|parameter>" +
		"</|DSML|invoke></|DSML|tool_calls>"
}

func newAgent(script func(int, Turn) string) (*Agent, *MockProvider) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})
	mp := &MockProvider{Script: script, ChunkRunes: 5}
	return New(Config{Provider: mp, Tools: reg, System: "sys"}), mp
}

func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			t.Fatal("run did not finish")
		}
	}
}

func kinds(evs []Event) []EventKind {
	var ks []EventKind
	var last EventKind = -1
	for _, e := range evs {
		if e.Kind != last { // collapse runs of text deltas
			ks = append(ks, e.Kind)
		}
		last = e.Kind
	}
	return ks
}

func textOf(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == EvText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func TestToolLoop(t *testing.T) {
	var prompts []string
	var parents []*int
	var mu sync.Mutex
	a, mp := newAgent(func(n int, tn Turn) string {
		mu.Lock()
		prompts = append(prompts, tn.Prompt)
		parents = append(parents, tn.ParentID)
		mu.Unlock()
		if n == 0 {
			return "Checking.\n" + call("echo", "hi")
		}
		return "All done."
	})

	evs := drain(t, a.Run(context.Background(), "do it"))

	want := []EventKind{EvText, EvToolCall, EvToolResult, EvText, EvDone}
	got := kinds(evs)
	if len(got) != len(want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event kinds = %v, want %v", got, want)
		}
	}
	if textOf(evs) != "Checking.\nAll done." {
		t.Errorf("text = %q", textOf(evs))
	}
	for _, e := range evs {
		if e.Kind == EvToolResult && (e.Result.Output != "echo:hi" || e.Call.Name != "echo") {
			t.Errorf("tool result = %+v", e.Result)
		}
	}

	if mp.turn != 2 || len(prompts) != 2 {
		t.Fatalf("provider turns = %d", mp.turn)
	}
	// First message carries the instructions and tools; the second only the result.
	if !strings.Contains(prompts[0], "sys") || !strings.Contains(prompts[0], "do it") || !strings.Contains(prompts[0], `"name":"echo"`) {
		t.Errorf("first prompt = %q", prompts[0])
	}
	if prompts[1] != "<tool_result>\necho:hi\n</tool_result>" {
		t.Errorf("second prompt = %q", prompts[1])
	}
	// The reply id from turn one becomes the parent of turn two.
	if parents[0] != nil || parents[1] == nil || *parents[1] != 2 {
		t.Errorf("parents = %v", parents)
	}
}

func TestSecondRunSendsOnlyTheNewMessage(t *testing.T) {
	var prompts []string
	a, _ := newAgent(func(n int, tn Turn) string {
		prompts = append(prompts, tn.Prompt)
		return "ok"
	})
	drain(t, a.Run(context.Background(), "first"))
	drain(t, a.Run(context.Background(), "second"))
	if prompts[1] != "second" {
		t.Errorf("second prompt = %q", prompts[1])
	}
	if len(a.History()) != 4 { // user, assistant, user, assistant
		t.Errorf("history = %+v", a.History())
	}
}

func TestUnknownToolIsReportedBackToTheModel(t *testing.T) {
	var second string
	a, _ := newAgent(func(n int, tn Turn) string {
		if n == 0 {
			return call("nope", "x")
		}
		second = tn.Prompt
		return "sorry"
	})
	evs := drain(t, a.Run(context.Background(), "go"))
	if !strings.Contains(second, "Error: unknown tool") || !strings.Contains(second, "echo") {
		t.Errorf("second prompt = %q", second)
	}
	if evs[len(evs)-1].Kind != EvDone {
		t.Errorf("last event = %+v", evs[len(evs)-1])
	}
}

func TestUnreadableCallIsRepairedThenSucceeds(t *testing.T) {
	var second string
	a, _ := newAgent(func(n int, tn Turn) string {
		switch n {
		case 0:
			return "<|DSML|tool_calls>garbage</|DSML|tool_calls>"
		case 1:
			second = tn.Prompt
			return call("echo", "again")
		}
		return "fixed"
	})
	evs := drain(t, a.Run(context.Background(), "go"))
	if !strings.Contains(second, "could not be executed") {
		t.Errorf("repair prompt = %q", second)
	}
	if last := evs[len(evs)-1]; last.Kind != EvDone {
		t.Errorf("last event = %+v", last)
	}
}

func TestUnreadableCallGivesUpAfterRepairs(t *testing.T) {
	a, _ := newAgent(func(n int, tn Turn) string { return "<|DSML|tool_calls>garbage</|DSML|tool_calls>" })
	evs := drain(t, a.Run(context.Background(), "go"))
	last := evs[len(evs)-1]
	if last.Kind != EvError || last.Err == nil {
		t.Fatalf("last event = %+v", last)
	}
}

func TestStepLimit(t *testing.T) {
	a, _ := newAgent(func(n int, tn Turn) string { return call("echo", "again") })
	a.cfg.MaxSteps = 3
	evs := drain(t, a.Run(context.Background(), "go"))
	last := evs[len(evs)-1]
	if last.Kind != EvError || !strings.Contains(last.Err.Error(), "3 steps") {
		t.Fatalf("last event = %+v", last)
	}
}

type failProvider struct{ err error }

func (f failProvider) Send(ctx context.Context, t Turn) (<-chan StreamEvent, error) { return nil, f.err }

func TestProviderErrorIsReported(t *testing.T) {
	a := New(Config{Provider: failProvider{errors.New("no bridge")}})
	evs := drain(t, a.Run(context.Background(), "hi"))
	if len(evs) != 1 || evs[0].Kind != EvError || evs[0].Err.Error() != "no bridge" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestEarlyEndStatusIsAnError(t *testing.T) {
	p := scriptedStream{events: []StreamEvent{
		{Kind: StreamReady, MessageID: 2},
		{Kind: StreamText, Text: "cut"},
		{Kind: StreamEnd, Text: "CONTENT_FILTER"},
	}}
	a := New(Config{Provider: p})
	evs := drain(t, a.Run(context.Background(), "hi"))
	last := evs[len(evs)-1]
	if last.Kind != EvError || !strings.Contains(last.Err.Error(), "CONTENT_FILTER") {
		t.Fatalf("last event = %+v", last)
	}
}

type scriptedStream struct{ events []StreamEvent }

func (s scriptedStream) Send(ctx context.Context, t Turn) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// A provider that never answers must not keep the run alive once cancelled.
type hangProvider struct{}

func (hangProvider) Send(ctx context.Context, t Turn) (<-chan StreamEvent, error) {
	return make(chan StreamEvent), nil
}

func TestCancelStopsARun(t *testing.T) {
	a := New(Config{Provider: hangProvider{}})
	ctx, cancel := context.WithCancel(context.Background())
	ch := a.Run(ctx, "hi")
	time.Sleep(20 * time.Millisecond)
	cancel()
	evs := drain(t, ch) // the channel must close
	for _, e := range evs {
		if e.Kind == EvError {
			t.Errorf("a cancelled run must not report an error: %+v", e)
		}
	}
}

func TestDemoScriptRunsEndToEnd(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	a := New(Config{Provider: &MockProvider{Script: DemoScript}, Tools: reg})
	t.Chdir("../..") // module root, where go.mod lives
	evs := drain(t, a.Run(context.Background(), "hi"))
	sawResult := false
	for _, e := range evs {
		if e.Kind == EvToolResult {
			sawResult = !e.Result.IsError && strings.Contains(e.Result.Output, "module kai")
		}
	}
	if !sawResult {
		t.Errorf("read_file did not return go.mod: %+v", evs)
	}
	if evs[len(evs)-1].Kind != EvDone {
		t.Errorf("last event = %+v", evs[len(evs)-1])
	}
}
