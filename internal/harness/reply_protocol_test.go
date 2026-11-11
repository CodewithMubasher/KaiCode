package harness

import (
	"context"
	"strings"
	"testing"

	"kai/internal/tools"
)

// jsonMock is a web-chat provider that speaks the strict JSON protocol.
type jsonMock struct{ inner *MockProvider }

func (j jsonMock) Send(ctx context.Context, t Turn) (<-chan StreamEvent, error) {
	return j.inner.Send(ctx, t)
}

func (j jsonMock) ToolProtocol() ToolProtocol { return ProtocolJSON }

func TestProtocolOf(t *testing.T) {
	cases := []struct {
		p    Provider
		want ToolProtocol
	}{
		{nil, ProtocolDSML},
		{&MockProvider{}, ProtocolDSML},
		{failProvider{}, ProtocolDSML},
		{jsonMock{}, ProtocolJSON},
	}
	for _, c := range cases {
		if got := ProtocolOf(c.p); got != c.want {
			t.Errorf("ProtocolOf(%T) = %v, want %v", c.p, got, c.want)
		}
	}
}

// The JSON path must build the JSON prompt, parse the JSON call, run the tool
// and send the result back in the JSON protocol, all without DSML anywhere.
func TestJSONProtocolEndToEnd(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(echoTool{})

	var first, second string
	inner := &MockProvider{ChunkRunes: 3, Script: func(n int, tn Turn) string {
		switch n {
		case 0:
			first = tn.Prompt
			return "I'll echo it.\n<tool_call>\n{\"name\":\"echo\",\"arguments\":{\"text\":\"hi\"}}\n</tool_call>"
		case 1:
			second = tn.Prompt
			return "Done."
		}
		return "Done."
	}}
	a := New(Config{Provider: jsonMock{inner}, Tools: reg, System: "sys"})

	evs := drain(t, a.Run(context.Background(), "do it"))

	if got := textOf(evs); got != "I'll echo it.\nDone." {
		t.Errorf("visible text = %q", got)
	}
	if !strings.Contains(first, "<tool_call>") || strings.Contains(first, "<|DSML|") {
		t.Errorf("first prompt does not teach the JSON protocol:\n%s", first)
	}
	if !strings.Contains(first, "sys") || !strings.Contains(first, "do it") || !strings.Contains(first, `"name":"echo"`) {
		t.Errorf("first prompt is missing instructions, tools or request:\n%s", first)
	}
	if !strings.Contains(second, `<tool_result name="echo">`) || !strings.Contains(second, "echo:hi") {
		t.Errorf("result message = %q", second)
	}
	if !strings.Contains(second, "always available") || !strings.Contains(second, "echo(text") {
		t.Errorf("result message does not remind about the tools:\n%s", second)
	}
	if evs[len(evs)-1].Kind != EvDone {
		t.Errorf("last event = %+v", evs[len(evs)-1])
	}
}
