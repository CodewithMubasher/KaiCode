package jsoncall

import (
	"strings"
	"testing"

	"kai/internal/tools"
)

const reasonReply = "I'll read the config to find the port.\n" +
	"<tool_call>\n" +
	`{"name":"read_file","arguments":{"path":"config.toml"}}` + "\n" +
	"</tool_call>"

func TestParseReasonAndCall(t *testing.T) {
	visible, calls, warns := Parse(reasonReply)
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
	if got := strings.TrimSpace(visible); got != "I'll read the config to find the port." {
		t.Errorf("visible text = %q", visible)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Name != "read_file" {
		t.Errorf("name = %q", calls[0].Name)
	}
	if path, _ := tools.StringArg(calls[0].Args, "path"); path != "config.toml" {
		t.Errorf("path = %q", path)
	}
	for _, bad := range []string{"<tool_call>", "</tool_call>", `"arguments"`, "{"} {
		if strings.Contains(visible, bad) {
			t.Errorf("raw markup leaked into the visible text %q: %q", bad, visible)
		}
	}
}

// Every marker can arrive split across chunks; nothing may be released early.
func TestParseChunkedDoesNotLeak(t *testing.T) {
	p := NewParser()
	var got strings.Builder
	for _, r := range reasonReply {
		got.WriteString(p.Feed(string(r)))
	}
	got.WriteString(p.Finish())
	if strings.Contains(got.String(), "<") {
		t.Fatalf("markup leaked into %q", got.String())
	}
	calls := p.Calls()
	if len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("calls = %#v, warnings = %v", calls, p.Warnings())
	}
}

// The model is trained on OpenAI wire calls, so accept that shape too.
func TestParseOpenAIWireCall(t *testing.T) {
	reply := `<tool_call>{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"go version\"}"}}</tool_call>`
	visible, calls, warns := Parse(reply)
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
	if strings.TrimSpace(visible) != "" {
		t.Errorf("visible text = %q", visible)
	}
	if len(calls) != 1 || calls[0].Name != "shell" {
		t.Fatalf("calls = %#v", calls)
	}
	if cmd, _ := tools.StringArg(calls[0].Args, "command"); cmd != "go version" {
		t.Errorf("command = %q", cmd)
	}
}

func TestParseMultipleCallsAndArrays(t *testing.T) {
	reply := "First.\n<tool_call>\n{\"name\":\"a\",\"arguments\":{\"n\":1}}\n</tool_call>\n" +
		"Second.\n<tool_call>{\"tool_calls\":[{\"name\":\"b\",\"arguments\":{}}," +
		`{"function":{"name":"c","arguments":"{\"n\":2}"}}]}</tool_call>`
	visible, calls, warns := Parse(reply)
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Name != "a" || calls[1].Name != "b" || calls[2].Name != "c" {
		t.Errorf("names = %q %q %q", calls[0].Name, calls[1].Name, calls[2].Name)
	}
	if !strings.Contains(visible, "First.") || !strings.Contains(visible, "Second.") {
		t.Errorf("reasons were dropped: %q", visible)
	}
}

// A model may wrap the JSON in a Markdown fence; the fence must not leak.
func TestParseStripsFence(t *testing.T) {
	reply := "<tool_call>\n```json\n{\"name\":\"a\",\"arguments\":{\"path\":\"x\"}}\n```\n</tool_call>"
	visible, calls, warns := Parse(reply)
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
	if len(calls) != 1 || calls[0].Name != "a" {
		t.Fatalf("calls = %#v", calls)
	}
	if strings.Contains(visible, "```") {
		t.Errorf("fence leaked: %q", visible)
	}
}

func TestParseBadJSONWarns(t *testing.T) {
	for _, reply := range []string{
		"<tool_call>{not json}</tool_call>",
		"<tool_call>   </tool_call>",
		"<tool_call>{\"arguments\":{}}</tool_call>",
	} {
		_, calls, warns := Parse(reply)
		if len(calls) != 0 {
			t.Errorf("%q produced calls %#v", reply, calls)
		}
		if len(warns) == 0 {
			t.Errorf("%q produced no warning", reply)
		}
	}
}

func TestParseUnclosedBlockStillRuns(t *testing.T) {
	reply := "<tool_call>\n{\"name\":\"a\",\"arguments\":{\"path\":\"x\"}}\n"
	visible, calls, warns := Parse(reply)
	if len(calls) != 1 || calls[0].Name != "a" {
		t.Fatalf("calls = %#v, warnings = %v", calls, warns)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "never closed") {
		t.Errorf("warnings = %v", warns)
	}
	if strings.Contains(visible, "<") {
		t.Errorf("markup leaked: %q", visible)
	}
}

// A reply cut off in the middle of a marker must not leak the raw tag.
func TestParseTruncatedMarkerDoesNotLeak(t *testing.T) {
	for _, reply := range []string{
		"I'll read it.\n<tool_call",
		"I'll read it.\n<tool_call>{\"name\":\"a\",\"arg",
		"Done.\n</tool_call>",
	} {
		visible, _, warns := Parse(reply)
		if strings.Contains(visible, "<") {
			t.Errorf("%q leaked markup: %q", reply, visible)
		}
		if len(warns) == 0 {
			t.Errorf("%q produced no warning", reply)
		}
	}
}

func TestPromptTeachesStrictJSONAndTools(t *testing.T) {
	specs := []tools.Spec{{
		Name: "edit_file", Description: "Edit a file.",
		Params: []tools.Param{
			{Name: "path", Type: "string", Required: true, Example: "src/main.go"},
			{Name: "old_string", Type: "string", Example: "old"},
			{Name: "new_string", Type: "string", Required: true, Example: "new"},
		},
	}}
	got := FirstPrompt("Be brief.", specs, "do it")
	for _, want := range []string{
		"<tool_call>", `{"name":"edit_file","arguments":{`, "reason",
		"ALWAYS available", "Be brief.", "do it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<|DSML|") {
		t.Errorf("prompt teaches DSML:\n%s", got)
	}
}

func TestEncodeResultsRemindsAboutEveryTool(t *testing.T) {
	specs := []tools.Spec{
		{Name: "read_file", Description: "read", Params: []tools.Param{{Name: "path", Type: "string", Required: true}}},
		{Name: "edit_file", Description: "edit", Params: []tools.Param{{Name: "path", Type: "string", Required: true}}},
	}
	got := EncodeResults([]tools.Result{
		{Name: "read_file", Output: "one"},
		{Name: "edit_file", Output: "bad", IsError: true},
	}, specs)
	for _, want := range []string{
		`<tool_result name="read_file">`,
		`<tool_result name="edit_file" status="error">`,
		"Error: bad",
		"Continue the task",
		"always available",
		"edit_file(",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result message is missing %q:\n%s", want, got)
		}
	}
}

func TestRepairPromptUsesJSONFormat(t *testing.T) {
	got := RepairPrompt("the JSON was invalid")
	for _, want := range []string{"could not be executed", "<tool_call>", `"name":"TOOL_NAME"`, "all four tools"} {
		if !strings.Contains(got, want) {
			t.Errorf("repair prompt is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<|DSML|") {
		t.Errorf("repair prompt teaches DSML:\n%s", got)
	}
}
