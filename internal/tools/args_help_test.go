package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAliasesAreAccepted(t *testing.T) {
	r := NewRegistry()
	r.Register(Shell{})
	res := r.Execute(context.Background(), Call{Name: "shell", Args: map[string]any{"cmd": "echo aliased"}}, 30*time.Second)
	if res.IsError || !strings.Contains(res.Output, "aliased") {
		t.Errorf("result = %+v", res)
	}
}

func TestMissingParameterTeachesTheModel(t *testing.T) {
	r := NewRegistry()
	r.Register(Shell{})
	res := r.Execute(context.Background(), Call{Name: "shell", Args: map[string]any{"wrong": "x"}}, time.Second)
	if !res.IsError {
		t.Fatal("expected an error")
	}
	for _, want := range []string{`missing required parameter "command"`, "You sent only these parameters: wrong", "Correct usage: shell(command (string, required)", `Example arguments: {"command":"ls -la"}`} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output lacks %q:\n%s", want, res.Output)
		}
	}
	if strings.Contains(res.Short, "Correct usage") || !strings.Contains(res.Short, "command") {
		t.Errorf("short = %q", res.Short)
	}
}

func TestLooseTypesAreFixed(t *testing.T) {
	r := NewRegistry()
	r.Register(Shell{})
	res := r.Execute(context.Background(), Call{Name: "shell", Args: map[string]any{"command": []any{"echo", "listed"}}}, 30*time.Second)
	if res.IsError || !strings.Contains(res.Output, "listed") {
		t.Errorf("result = %+v", res)
	}
}

func TestNormalizeRenamesForTheUI(t *testing.T) {
	r := NewRegistry()
	r.Register(EditFile{})
	c := r.Normalize(Call{Name: "edit_file", Args: map[string]any{"file_path": "a", "old_str": "b", "new_str": "c"}})
	if c.Args["path"] != "a" || c.Args["old_string"] != "b" || c.Args["new_string"] != "c" {
		t.Errorf("args = %v", c.Args)
	}
}
