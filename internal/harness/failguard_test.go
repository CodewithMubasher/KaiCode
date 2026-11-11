package harness

import (
	"strings"
	"testing"

	"kai/internal/tools"
)

func TestFailGuardEscalatesAndStops(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.Shell{})
	var g failGuard
	c := tools.Call{Name: "shell", Args: map[string]any{}}
	stopped := false
	var last tools.Result
	for i := 0; i < stopAfterSame; i++ {
		last = tools.Result{IsError: true, Output: "boom"}
		stopped = g.note(reg, c, &last)
	}
	if !stopped {
		t.Error("the request should stop after repeated identical failures")
	}
	if !strings.Contains(last.Output, "STOP:") || !strings.Contains(last.Output, "shell(command") {
		t.Errorf("output = %q", last.Output)
	}
	ok := tools.Result{Output: "fine"}
	if g.note(reg, c, &ok) || ok.Output != "fine" {
		t.Error("a success must reset the guard and stay untouched")
	}
}
