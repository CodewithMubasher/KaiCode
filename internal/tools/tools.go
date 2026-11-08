// Package tools holds the tool interface, the registry the harness dispatches
// through, and the built-in tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxOutput is the most bytes of tool output sent back to the model.
const MaxOutput = 32 * 1024

// Param describes one tool parameter.
type Param struct {
	Name        string
	Type        string // JSON schema type: string, integer, number, boolean, array, object
	Description string
	Required    bool
}

// Spec describes a tool to the model.
type Spec struct {
	Name        string
	Description string
	Params      []Param
}

// JSON renders the spec as an OpenAI-style function schema, which is the
// shape DeepSeek's prompt format expects tool definitions in.
func (s Spec) JSON() string {
	props := map[string]any{}
	required := []string{}
	for _, p := range s.Params {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		props[p.Name] = map[string]any{"type": typ, "description": p.Description}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	b, _ := json.Marshal(map[string]any{
		"name":        s.Name,
		"description": s.Description,
		"parameters": map[string]any{
			"type":       "object",
			"properties": props,
			"required":   required,
		},
	})
	return string(b)
}

// Tool is something the model can call.
type Tool interface {
	Spec() Spec
	Run(ctx context.Context, args map[string]any) (string, error)
}

// Call is one tool call parsed from a model reply.
type Call struct {
	ID   string
	Name string
	Args map[string]any
}

// Result is what a call produced, sent back to the model.
type Result struct {
	ID      string
	Name    string
	Output  string
	IsError bool
}

// Registry holds the tools available to the agent.
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool (replacing one with the same name).
func (r *Registry) Register(t Tool) {
	name := t.Spec().Name
	if _, ok := r.tools[name]; !ok {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Specs lists every tool's spec in registration order.
func (r *Registry) Specs() []Spec {
	out := make([]Spec, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Spec())
	}
	return out
}

// Names lists tool names, sorted.
func (r *Registry) Names() []string {
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

// Execute runs one call. It never fails: problems (unknown tool, missing
// argument, error, panic, timeout) come back as an error Result so the model
// can read them and correct itself.
func (r *Registry) Execute(ctx context.Context, c Call, timeout time.Duration) Result {
	res := Result{ID: c.ID, Name: c.Name}
	t, ok := r.tools[c.Name]
	if !ok {
		res.IsError = true
		res.Output = fmt.Sprintf("unknown tool %q. Available tools: %s", c.Name, strings.Join(r.Names(), ", "))
		return res
	}
	for _, p := range t.Spec().Params {
		if _, has := c.Args[p.Name]; p.Required && !has {
			res.IsError = true
			res.Output = fmt.Sprintf("missing required parameter %q for tool %q", p.Name, c.Name)
			return res
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	out, err := safeRun(ctx, t, c.Args)
	if err != nil {
		res.IsError = true
		if out != "" {
			out += "\n"
		}
		out += err.Error()
	}
	res.Output = Truncate(out, MaxOutput)
	return res
}

func safeRun(ctx context.Context, t Tool, args map[string]any) (out string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("tool panicked: %v", rec)
		}
	}()
	if args == nil {
		args = map[string]any{}
	}
	return t.Run(ctx, args)
}

// Truncate cuts s to at most max bytes (on a character boundary) and says
// how much was dropped.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[output truncated: %d more bytes]", len(s)-cut)
}

// StringArg reads a string argument.
func StringArg(args map[string]any, name string) (string, bool) {
	s, ok := args[name].(string)
	return s, ok
}
