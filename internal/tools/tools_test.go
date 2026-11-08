package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fake struct {
	run func(ctx context.Context, args map[string]any) (string, error)
}

func (f fake) Spec() Spec {
	return Spec{Name: "fake", Description: "d", Params: []Param{{Name: "x", Required: true}}}
}
func (f fake) Run(ctx context.Context, args map[string]any) (string, error) { return f.run(ctx, args) }

func registry(run func(ctx context.Context, args map[string]any) (string, error)) *Registry {
	r := NewRegistry()
	r.Register(fake{run})
	return r
}

func TestExecuteOK(t *testing.T) {
	r := registry(func(ctx context.Context, a map[string]any) (string, error) { return "hi " + a["x"].(string), nil })
	res := r.Execute(context.Background(), Call{ID: "1", Name: "fake", Args: map[string]any{"x": "there"}}, time.Second)
	if res.IsError || res.Output != "hi there" || res.ID != "1" {
		t.Errorf("result = %+v", res)
	}
}

func TestExecuteUnknownAndMissing(t *testing.T) {
	r := registry(nil)
	if res := r.Execute(context.Background(), Call{Name: "nope"}, time.Second); !res.IsError || !strings.Contains(res.Output, "fake") {
		t.Errorf("unknown tool result = %+v", res)
	}
	if res := r.Execute(context.Background(), Call{Name: "fake"}, time.Second); !res.IsError || !strings.Contains(res.Output, `"x"`) {
		t.Errorf("missing parameter result = %+v", res)
	}
}

func TestExecuteErrorAndPanic(t *testing.T) {
	r := registry(func(ctx context.Context, a map[string]any) (string, error) { return "partial", errors.New("boom") })
	res := r.Execute(context.Background(), Call{Name: "fake", Args: map[string]any{"x": 1}}, time.Second)
	if !res.IsError || !strings.Contains(res.Output, "partial") || !strings.Contains(res.Output, "boom") {
		t.Errorf("error result = %+v", res)
	}

	r = registry(func(ctx context.Context, a map[string]any) (string, error) { panic("oops") })
	res = r.Execute(context.Background(), Call{Name: "fake", Args: map[string]any{"x": 1}}, time.Second)
	if !res.IsError || !strings.Contains(res.Output, "oops") {
		t.Errorf("panic result = %+v", res)
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := Truncate(s, 5)
	if !strings.HasPrefix(got, "éé") || strings.HasPrefix(got, "ééé") || !strings.Contains(got, "truncated") {
		t.Errorf("Truncate = %q", got)
	}
	if Truncate("short", 100) != "short" {
		t.Error("short strings must pass through")
	}
}

func TestSpecJSON(t *testing.T) {
	got := Spec{Name: "t", Description: "d", Params: []Param{{Name: "p", Required: true}}}.JSON()
	for _, want := range []string{`"name":"t"`, `"required":["p"]`, `"type":"string"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON %s is missing %s", got, want)
		}
	}
}
