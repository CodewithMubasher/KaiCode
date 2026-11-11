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
	// Aliases are other names models commonly use for this parameter
	// ("cmd" for "command"). They are accepted and renamed before the call
	// is checked, so a harmless naming slip does not cost a round trip.
	Aliases []string
	// Example is a realistic value, used in the usage examples that teach
	// the model how to call the tool.
	Example string
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

// Usage describes the tool's parameters on one line, for error messages.
func (s Spec) Usage() string {
	parts := make([]string, 0, len(s.Params))
	for _, p := range s.Params {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		req := "optional"
		if p.Required {
			req = "required"
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", p.Name, typ, req))
	}
	return s.Name + "(" + strings.Join(parts, ", ") + ")"
}

// ExampleArgs is a valid example of the call's arguments as JSON: every
// required parameter, filled with its Example value.
func (s Spec) ExampleArgs() string {
	m := map[string]any{}
	for _, p := range s.Params {
		if !p.Required {
			continue
		}
		ex := p.Example
		if ex == "" {
			ex = "<" + p.Name + ">"
		}
		switch p.Type {
		case "integer", "number":
			var n float64
			if _, err := fmt.Sscan(ex, &n); err == nil {
				m[p.Name] = n
				continue
			}
		case "boolean":
			m[p.Name] = ex == "true"
			continue
		}
		m[p.Name] = ex
	}
	b, _ := json.Marshal(m)
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
	// Short is a one-line summary of an error for the person using Kai, set
	// when Output is long guidance meant for the model. Empty otherwise.
	Short string
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

// Normalize returns the call with parameter aliases renamed and loose types
// fixed, as Execute will run it. The UI shows this version, so a call that
// used "cmd" for "command" is still drawn properly. A call that cannot be
// fixed is returned unchanged.
func (r *Registry) Normalize(c Call) Call {
	t, ok := r.tools[c.Name]
	if !ok {
		return c
	}
	if args, problem := normalizeArgs(t.Spec(), c.Args); problem == "" {
		c.Args = args
	}
	return c
}

// Help is the correct usage and an example call of a tool, or "" for an
// unknown tool. It is added to errors that happen before a call can run.
func (r *Registry) Help(name string) string {
	t, ok := r.tools[name]
	if !ok {
		return ""
	}
	s := t.Spec()
	return "Correct usage: " + s.Usage() + "\nExample arguments: " + s.ExampleArgs()
}

// Execute runs one call. It never fails: problems (unknown tool, missing
// argument, error, panic, timeout) come back as an error Result so the model
// can read them and correct itself.
func (r *Registry) Execute(ctx context.Context, c Call, timeout time.Duration) Result {
	res := Result{ID: c.ID, Name: c.Name}
	t, ok := r.tools[c.Name]
	if !ok {
		res.IsError = true
		res.Output = fmt.Sprintf("unknown tool %q. Available tools: %s. Call one of these by its exact name; do not invent tool names.", c.Name, strings.Join(r.Names(), ", "))
		res.Short = fmt.Sprintf("unknown tool %q", c.Name)
		return res
	}
	spec := t.Spec()
	args, problem := normalizeArgs(spec, c.Args)
	if problem != "" {
		res.IsError = true
		res.Short = problem
		res.Output = problem + ".\n" + argHelp(spec, c.Args)
		return res
	}
	c.Args = args
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
		if out == "" {
			res.Short = err.Error() // nothing partial to show: the person gets the reason alone
		}
		out += err.Error()
		if strings.Contains(err.Error(), "parameter") {
			out += "\n" + argHelp(spec, c.Args)
		}
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

// normalizeArgs checks a call's arguments against the spec. It renames known
// aliases, turns numbers, booleans and string lists into text where the
// parameter is text, and reports the first problem (missing or wrong type) as
// a short sentence. The caller's map is never changed.
func normalizeArgs(spec Spec, in map[string]any) (map[string]any, string) {
	args := make(map[string]any, len(in))
	for k, v := range in {
		args[k] = v
	}
	for _, p := range spec.Params {
		if _, has := args[p.Name]; !has {
			for _, a := range p.Aliases {
				if v, ok := args[a]; ok {
					args[p.Name] = v
					delete(args, a)
					break
				}
			}
		}
		v, has := args[p.Name]
		if !has || v == nil {
			if p.Required {
				return nil, fmt.Sprintf("missing required parameter %q for tool %q", p.Name, spec.Name)
			}
			delete(args, p.Name)
			continue
		}
		if p.Type == "" || p.Type == "string" {
			switch x := v.(type) {
			case string:
			case float64, bool:
				args[p.Name] = fmt.Sprint(x)
			case []any:
				parts := make([]string, 0, len(x))
				for _, e := range x {
					s, ok := e.(string)
					if !ok {
						return nil, fmt.Sprintf("parameter %q of tool %q must be a string, not a list", p.Name, spec.Name)
					}
					parts = append(parts, s)
				}
				args[p.Name] = strings.Join(parts, " ")
			default:
				return nil, fmt.Sprintf("parameter %q of tool %q must be a string", p.Name, spec.Name)
			}
		}
	}
	return args, ""
}

// argHelp is the part of an argument error that teaches the model: what it
// sent, the correct usage and a working example.
func argHelp(spec Spec, sent map[string]any) string {
	got := "no arguments"
	if len(sent) > 0 {
		keys := make([]string, 0, len(sent))
		for k := range sent {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		got = "only these parameters: " + strings.Join(keys, ", ")
	}
	return fmt.Sprintf("You sent %s.\nCorrect usage: %s\nExample arguments: %s\n"+
		"Send the call again with every required parameter, spelled exactly as above. Do not repeat the same call unchanged.",
		got, spec.Usage(), spec.ExampleArgs())
}
