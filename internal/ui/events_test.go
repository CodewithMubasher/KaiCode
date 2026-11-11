package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"kai/internal/harness"
	"kai/internal/tools"
	"kai/internal/ui/components"
)

func testModel() Model {
	return Model{chat: components.NewChat(110, 60).AppendUser("go").StartReply()}
}

func view(m Model) string { return ansi.Strip(m.chat.View()) }

func call(name string, args map[string]any) *tools.Call {
	return &tools.Call{ID: "c1", Name: name, Args: args}
}

func TestSuccessfulEditShowsOneBlock(t *testing.T) {
	m := testModel()
	args := map[string]any{"path": "x.go", "old_string": "", "new_string": "package x\n"}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolProgress, Progress: &harness.ToolProgress{Name: "edit_file", Path: "x.go", Lines: 2, Creating: true}})
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("edit_file", args)})
	if v := view(m); !strings.Contains(v, "creating file") || strings.Count(v, "x.go") != 1 {
		t.Fatalf("while writing, expected one creating line:\n%s", v)
	}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("edit_file", args), Result: &tools.Result{Output: "ok"}})
	v := view(m)
	if n := strings.Count(v, "Created x.go"); n != 1 {
		t.Errorf("'Created x.go' appears %d times:\n%s", n, v)
	}
	if strings.Contains(v, "creating file") {
		t.Errorf("progress line survived the result:\n%s", v)
	}
}

func TestFailedEditLeavesNoProgressLine(t *testing.T) {
	m := testModel()
	args := map[string]any{"path": "x.go", "old_string": "a", "new_string": "b"}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("edit_file", args)})
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("edit_file", args), Result: &tools.Result{Output: "not found", IsError: true}})
	v := view(m)
	if strings.Contains(v, "editing file") {
		t.Errorf("progress line left behind after a failed edit:\n%s", v)
	}
	if !strings.Contains(v, "Could not edit x.go") || strings.Count(v, "not found") != 1 {
		t.Errorf("expected one failure node with a one-line reason:\n%s", v)
	}
	if strings.Contains(v, "Error:") {
		t.Errorf("a raw tool result must not be drawn:\n%s", v)
	}
}

func TestCreateFileShowsFullContentAsNode(t *testing.T) {
	m := testModel()
	args := map[string]any{"path": "a/b.go", "content": "package b\n\nfunc B() {}\n"}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("create_file", args)})
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("create_file", args), Result: &tools.Result{Output: `successfully created file "a/b.go"`}})
	v := view(m)
	if !strings.Contains(v, "Created a/b.go") {
		t.Errorf("missing the heading:\n%s", v)
	}
	// Once the tool call is done the node is collapsed: heading only.
	if strings.Contains(v, "package b") {
		t.Errorf("a finished create_file node should be collapsed:\n%s", v)
	}
	if strings.Contains(v, "successfully created") || strings.Contains(v, "create_file") {
		t.Errorf("tool result or raw call leaked into the chat:\n%s", v)
	}
}

func TestEditShowsDiffNode(t *testing.T) {
	m := testModel()
	args := map[string]any{"path": "x.go", "old_string": "a := 1", "new_string": "a := 2"}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("edit_file", args)})
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("edit_file", args), Result: &tools.Result{Output: "Edited x.go: 1 replacement(s), first at line 7.\n- a := 1\n+ a := 2"}})
	v := view(m)
	for _, want := range []string{"Edited x.go", "- a := 1", "+ a := 2", "7"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestMissingParameterShowsOneLine(t *testing.T) {
	m := testModel()
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("shell", map[string]any{})})
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("shell", map[string]any{}),
		Result: &tools.Result{IsError: true, Output: "long guidance for the model\nCorrect usage: shell(...)", Short: `missing required parameter "command" for tool "shell"`}})
	v := view(m)
	if !strings.Contains(v, "missing required parameter") || strings.Contains(v, "Correct usage") {
		t.Errorf("the person should see only the short reason:\n%s", v)
	}
}

func TestShellCommandShowsRunningThenOneBlock(t *testing.T) {
	m := testModel()
	args := map[string]any{"command": "go test ./..."}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("shell", args)})
	if v := view(m); !strings.Contains(v, "running") {
		t.Fatalf("a running command should be visible:\n%s", v)
	}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("shell", args), Result: &tools.Result{Output: "ok"}})
	v := view(m)
	if strings.Contains(v, "running") || strings.Count(v, "go test ./...") != 1 {
		t.Errorf("expected exactly one finished block:\n%s", v)
	}
}

func TestReadFileShowsOnce(t *testing.T) {
	m := testModel()
	args := map[string]any{"path": "go.mod"}
	m = m.applyEvent(harness.Event{Kind: harness.EvToolCall, Call: call("read_file", args)})
	m = m.applyEvent(harness.Event{Kind: harness.EvToolResult, Call: call("read_file", args), Result: &tools.Result{Output: "module kai"}})
	if n := strings.Count(view(m), "Read file"); n != 1 {
		t.Errorf("'Read file' appears %d times:\n%s", n, view(m))
	}
}

func TestEachThoughtIsTimedFromItsOwnStart(t *testing.T) {
	m := testModel()
	m = m.applyEvent(harness.Event{Kind: harness.EvThinking, Text: "first"})
	if m.run.thinkAt.IsZero() {
		t.Fatal("a thought should have a start time")
	}
	m = m.applyEvent(harness.Event{Kind: harness.EvText, Text: "answer"})
	if !m.run.thinkAt.IsZero() {
		t.Error("the start time must be cleared when the thought ends, or the next one is timed from the first")
	}
}

// A whole run through the real agent and parser: a very long file created
// from a fast stream. Nothing may freeze, duplicate or overflow.
func TestLongFileCreationEndToEnd(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(tools.EditFile{Dir: dir})

	var body strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&body, "func f%d() int { return %d } // generated line\n", i, i)
	}
	reply := "Creating it.\n<|DSML|tool_calls>\n<|DSML|invoke name=\"edit_file\">\n" +
		"<|DSML|parameter name=\"path\" string=\"true\">big.go</|DSML|parameter>\n" +
		"<|DSML|parameter name=\"new_string\" string=\"true\">" + body.String() +
		"</|DSML|parameter>\n</|DSML|invoke>\n</|DSML|tool_calls>"
	mp := &harness.MockProvider{ChunkRunes: 16, Script: func(n int, _ harness.Turn) string {
		if n == 0 {
			return reply
		}
		return "All done."
	}}
	a := harness.New(harness.Config{Provider: mp, Tools: reg, ToolTimeout: 30 * time.Second})

	m := testModel()
	start := time.Now()
	events := 0
	for ev := range a.Run(context.Background(), "make a big file") {
		m = m.applyEvent(ev)
		m.chat = m.chat.SetSize(110, 40)
		events++
		if d := time.Since(start); d > 60*time.Second {
			t.Fatalf("the run is not finishing (%v, %d events)", d, events)
		}
	}
	for i := 0; i < 5; i++ {
		m.chat = m.chat.Tick()
	}
	took := time.Since(start)
	t.Logf("%d events for a %d KB file, %v", events, body.Len()/1024, took)

	if _, err := os.Stat(filepath.Join(dir, "big.go")); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	v := ansi.Strip(m.chat.ScrollToBottom().View())
	if n := strings.Count(v, "Created big.go"); n > 1 {
		t.Errorf("the created block appears %d times", n)
	}
	if !strings.Contains(v, "All done.") {
		t.Errorf("the final answer is missing:\n%s", v)
	}
	for i, r := range strings.Split(m.chat.View(), "\n") {
		if w := ansi.StringWidth(r); w != 110 {
			t.Fatalf("row %d is %d cells wide, want 110", i, w)
		}
	}
	if took > 30*time.Second {
		t.Errorf("creating a 1 MB file took %v", took)
	}
	if events > 5000 {
		t.Logf("note: %d events reached the UI", events)
	}
}
