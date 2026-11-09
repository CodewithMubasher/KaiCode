package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kai/internal/harness"
	"kai/internal/tools"
)

// scripted replies a model might send, in order: each ends with a DSML call
// block except the last. It also records what Kai sent back.
type scripted struct {
	replies []string
	sent    []string
}

func (s *scripted) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	s.sent = append(s.sent, t.Prompt)
	r := s.replies[len(s.sent)-1]
	ch := make(chan harness.StreamEvent, 4)
	ch <- harness.StreamEvent{Kind: harness.StreamSent}
	ch <- harness.StreamEvent{Kind: harness.StreamReady, MessageID: len(s.sent)}
	ch <- harness.StreamEvent{Kind: harness.StreamText, Text: r}
	ch <- harness.StreamEvent{Kind: harness.StreamEnd}
	close(ch)
	return ch, nil
}

func call(tool string, params ...string) string {
	var b strings.Builder
	b.WriteString("<|DSML|tool_calls>\n<|DSML|invoke name=\"" + tool + "\">\n")
	for i := 0; i+2 < len(params)+1; i += 3 {
		b.WriteString("<|DSML|parameter name=\"" + params[i] + "\" string=\"" + params[i+1] + "\">" + params[i+2] + "</|DSML|parameter>\n")
	}
	b.WriteString("</|DSML|invoke>\n</|DSML|tool_calls>")
	return b.String()
}

func TestAgentUsesShellAndEditFileEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scripted commands use POSIX shell syntax; the tools package tests cover PowerShell")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\r\n\r\nfunc main() {\r\n\tprintln(\"one\")\r\n}\r\n"), 0o644)

	p := &scripted{replies: []string{
		"Let me look.\n" + call("shell", "command", "true", "ls"),
		call("edit_file", "path", "true", "main.go", "old_string", "true", "println(\"one\")", "new_string", "true", "println(\"two\")"),
		call("shell", "command", "true", "mkdir sub && echo hi > sub/a.txt && grep -rn hi sub"),
		call("shell", "command", "true", "rm -rf sub && ls", "timeout_seconds", "false", "30"),
		"Done: changed one to two.",
	}}
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.Shell{})
	reg.Register(tools.EditFile{})
	a := harness.New(harness.Config{Provider: p, Tools: reg, System: "sys"})

	var calls, results, errs int
	var final string
	for ev := range a.Run(context.Background(), "change one to two") {
		switch ev.Kind {
		case harness.EvToolCall:
			calls++
		case harness.EvToolResult:
			results++
			if ev.Result.IsError {
				errs++
				t.Errorf("tool error: %s -> %s", ev.Call.Name, ev.Result.Output)
			}
		case harness.EvText:
			final += ev.Text
		case harness.EvError:
			t.Fatalf("agent error: %v", ev.Err)
		}
	}
	if calls != 4 || results != 4 || errs != 0 {
		t.Errorf("calls=%d results=%d errs=%d", calls, results, errs)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if string(b) != "package main\r\n\r\nfunc main() {\r\n\tprintln(\"two\")\r\n}\r\n" {
		t.Errorf("main.go = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Errorf("sub was not deleted: %v", err)
	}
	if !strings.Contains(final, "Done") {
		t.Errorf("final = %q", final)
	}
	// The first message carries the tool list including the new tools; later ones do not repeat it.
	if !strings.Contains(p.sent[0], `"name":"shell"`) || !strings.Contains(p.sent[0], `"name":"edit_file"`) {
		t.Errorf("first prompt lacks the new tools")
	}
	if strings.Contains(p.sent[1], "kai_instructions") {
		t.Errorf("instructions were repeated")
	}
	if !strings.Contains(p.sent[1], "<tool_result>") || !strings.Contains(p.sent[1], "main.go") || !strings.Contains(p.sent[1], "[exit code: 0]") {
		t.Errorf("second message = %q", p.sent[1])
	}
	if !strings.Contains(p.sent[2], "Edited main.go") {
		t.Errorf("edit result not reported: %q", p.sent[2])
	}
}

// The reply that used to leak into the TUI instead of running: double
// full-width bars, "calls" as the wrapper name, a BOM in front.
func TestMisspelledMarkupStillRunsTheTool(t *testing.T) {
	reply := "\ufeff\n\n<｜｜DSML｜｜ calls>\n" +
		"<｜｜DSML｜｜ invoke name=\"shell\">\n" +
		"<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">echo kai-ran-it</｜｜DSML｜｜ parameter>\n" +
		"</｜｜DSML｜｜ invoke>\n" +
		"</｜｜DSML｜｜ calls>"
	p := &scripted{replies: []string{reply, "It printed kai-ran-it."}}
	reg := tools.NewRegistry()
	reg.Register(tools.Shell{})
	a := harness.New(harness.Config{Provider: p, Tools: reg, System: "sys"})

	var ran bool
	var shown string
	for ev := range a.Run(context.Background(), "run it") {
		switch ev.Kind {
		case harness.EvToolResult:
			ran = strings.Contains(ev.Result.Output, "kai-ran-it")
		case harness.EvText:
			shown += ev.Text
		case harness.EvError:
			t.Fatalf("agent error: %v", ev.Err)
		}
	}
	if !ran {
		t.Error("the tool did not run")
	}
	if strings.Contains(shown, "DSML") || strings.Contains(shown, "invoke") {
		t.Errorf("markup reached the screen: %q", shown)
	}
	if len(p.sent) != 2 || !strings.Contains(p.sent[1], "kai-ran-it") {
		t.Errorf("the result was not sent back: %v", p.sent)
	}
}
