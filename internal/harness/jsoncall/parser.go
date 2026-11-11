// Package jsoncall reads and writes Kai's strict JSON tool-call protocol for
// web chats that do not expose native tool calling:
//
//	I'll read the config to find the port.
//	<tool_call>
//	{"name":"read_file","arguments":{"path":"config.toml"}}
//	</tool_call>
//
// It also accepts OpenAI wire-style calls inside the block (a "function"
// object with string arguments), so a model trained on the API format is not
// punished for using it. The parser hides every marker as it streams: raw
// markup must never reach the timeline.
package jsoncall

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
	"unsafe"

	"kai/internal/tools"
)

// Parser reads a model reply as it streams in. Feed it text deltas: it returns
// the text that is safe to show, holds back anything that might be the start
// of a tool-call marker, and collects the calls it finds.
type Parser struct {
	// raw is append-only, so buf can be a zero-copy view of it. Adding a chunk
	// stays O(chunk), even while a large file streams through a tool call.
	raw  []byte
	buf  string
	in   bool // inside a <tool_call> block
	call []tools.Call
	warn []string
	seq  int

	closeScan int
}

func NewParser() *Parser { return &Parser{} }

// Feed adds a chunk and returns the visible text it releases.
func (p *Parser) Feed(chunk string) string {
	p.push(chunk)
	return p.drain(false)
}

// Finish is called when the stream ends. It releases held-back text and
// salvages a block that was cut off.
func (p *Parser) Finish() string { return p.drain(true) }

// Calls returns the tool calls found so far.
func (p *Parser) Calls() []tools.Call { return p.call }

// Warnings lists problems found while parsing (bad JSON, empty blocks).
func (p *Parser) Warnings() []string { return p.warn }

var (
	// The strict form is <tool_call>...</tool_call>; the tolerant pattern also
	// accepts <tool_calls>, optional "|" bars and spaces, because models mix
	// the names up even when the instructions are explicit.
	openRe  = regexp.MustCompile(`(?i)<\s*\|?\s*tool_calls?\s*\|?\s*>`)
	closeRe = regexp.MustCompile(`(?i)<\s*/\s*\|?\s*tool_calls?\s*\|?\s*>`)

	// Model control tokens that sometimes leak into reply text.
	specialRe = regexp.MustCompile(`(?i)<\|?\s*(?:begin|end)[▁_ ]?of[▁_ ]?sentence\s*\|?>`)

	// A tool-call marker the reply was cut off before, or a stray closing
	// tag: none of it may reach the timeline.
	leftoverRe = regexp.MustCompile(`(?i)<\s*/?\s*\|?\s*tool_calls?\s*\|?\s*>?`)
)

func clean(s string) string {
	return specialRe.ReplaceAllString(strings.ReplaceAll(s, "\ufeff", ""), "")
}

func (p *Parser) nextID() string {
	p.seq++
	return fmt.Sprintf("call_%d", p.seq)
}

func (p *Parser) drain(final bool) string {
	var out strings.Builder
	for {
		if p.in {
			loc := p.findClose()
			if loc == nil {
				if final {
					p.addBlock(p.buf)
					p.warn = append(p.warn, "tool_call block was never closed (the reply ended early)")
					p.drop(len(p.buf))
					p.in, p.closeScan = false, 0
				}
				break
			}
			p.addBlock(p.buf[:loc[0]])
			p.drop(loc[1])
			p.in, p.closeScan = false, 0
			continue
		}

		if loc := openRe.FindStringIndex(p.buf); loc != nil {
			out.WriteString(clean(p.buf[:loc[0]]))
			p.drop(loc[1])
			p.in, p.closeScan = true, 0
			continue
		}

		// No marker yet. Release everything except a tail that could still
		// grow into one.
		if final {
			if loc := leftoverRe.FindStringIndex(p.buf); loc != nil {
				out.WriteString(clean(p.buf[:loc[0]]))
				p.warn = append(p.warn, "the reply ended with an incomplete tool_call marker")
				p.drop(len(p.buf))
				break
			}
		}
		keep := 0
		if !final {
			keep = partialTail(p.buf)
		}
		cut := len(p.buf) - keep
		out.WriteString(clean(p.buf[:cut]))
		p.drop(cut)
		break
	}
	return out.String()
}

// partialTail returns how many bytes at the end of s might be the start of a
// tool-call marker and so must wait for more text.
func partialTail(s string) int {
	i := strings.LastIndex(s, "<")
	if i < 0 {
		return 0
	}
	tail := s[i:]
	if len(tail) > 40 {
		return 0
	}
	if !utf8.ValidString(tail) {
		return len(tail)
	}
	compact := strings.ToLower(strings.Join(strings.Fields(tail), ""))
	for _, full := range []string{
		"<tool_call>", "<tool_calls>", "<|tool_call|>", "<|tool_calls|>",
		"</tool_call>", "</tool_calls>", "</|tool_call|>", "</|tool_calls|>",
	} {
		if strings.HasPrefix(full, compact) {
			return len(tail)
		}
	}
	return 0
}

// findClose looks for the closing tag, scanning only the new bytes (plus a
// small overlap for a tag split across chunks).
func (p *Parser) findClose() []int {
	const backoff = 64
	from := p.closeScan - backoff
	if from < 0 {
		from = 0
	}
	loc := closeRe.FindStringIndex(p.buf[from:])
	if loc == nil {
		p.closeScan = len(p.buf)
		return nil
	}
	return []int{loc[0] + from, loc[1] + from}
}

func (p *Parser) push(chunk string) {
	if chunk == "" {
		return
	}
	p.raw = append(p.raw, chunk...)
	p.view()
}

func (p *Parser) drop(n int) {
	if n >= len(p.raw) {
		p.raw = nil
	} else {
		p.raw = p.raw[n:]
	}
	p.view()
}

func (p *Parser) view() {
	if len(p.raw) == 0 {
		p.buf = ""
		return
	}
	p.buf = unsafe.String(&p.raw[0], len(p.raw))
}

// addBlock parses the body of one <tool_call> block.
func (p *Parser) addBlock(body string) {
	body = stripFence(strings.TrimSpace(body))
	if body == "" {
		p.warn = append(p.warn, "tool_call block was empty")
		return
	}
	values, err := decodeValues(body)
	before := len(p.call)
	for _, v := range values {
		p.addValue(v)
	}
	if err != nil {
		p.warn = append(p.warn, "tool_call block is not valid JSON: "+err.Error())
		return
	}
	if len(p.call) == before {
		p.warn = append(p.warn, "tool_call block contained no usable call")
	}
}

// stripFence removes a Markdown code fence a model may wrap around the JSON.
func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// decodeValues reads one or more JSON values from the block.
func decodeValues(body string) ([]any, error) {
	dec := json.NewDecoder(strings.NewReader(body))
	var out []any
	for {
		var v any
		err := dec.Decode(&v)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
}

func (p *Parser) addValue(v any) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			p.addValue(item)
		}
	case map[string]any:
		// OpenAI-style envelope: {"tool_calls":[...]} or {"calls":[...]}.
		for _, key := range []string{"tool_calls", "calls"} {
			if raw, ok := x[key]; ok {
				p.addValue(raw)
				return
			}
		}
		p.addCall(x)
	default:
		p.warn = append(p.warn, fmt.Sprintf("tool_call block held a %T, not a JSON object", v))
	}
}

func (p *Parser) addCall(m map[string]any) {
	raw := m
	if fn, ok := m["function"].(map[string]any); ok {
		raw = fn // {"type":"function","function":{"name":...,"arguments":...}}
	}
	name, _ := raw["name"].(string)
	if name == "" {
		name, _ = m["name"].(string)
	}
	if strings.TrimSpace(name) == "" {
		p.warn = append(p.warn, `tool_call has no "name"`)
		return
	}

	args := map[string]any{}
	switch a := raw["arguments"].(type) {
	case nil:
		if a2, ok := m["arguments"]; ok {
			args = coerceArgs(a2, name, &p.warn)
		}
	case map[string]any:
		args = a
	case string:
		if strings.TrimSpace(a) != "" {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(a), &parsed); err != nil {
				p.warn = append(p.warn, fmt.Sprintf("arguments of %q are not valid JSON: %v", name, err))
				return
			}
			if parsed != nil {
				args = parsed
			}
		}
	default:
		args = coerceArgs(a, name, &p.warn)
	}

	p.call = append(p.call, tools.Call{ID: p.nextID(), Name: name, Args: args})
}

func coerceArgs(v any, name string, warns *[]string) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return map[string]any{}
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			*warns = append(*warns, fmt.Sprintf("arguments of %q are not valid JSON: %v", name, err))
			return map[string]any{}
		}
		return parsed
	}
	*warns = append(*warns, fmt.Sprintf("arguments of %q must be a JSON object", name))
	return map[string]any{}
}

// Parse is the one-shot form: it returns the visible text and the calls of a
// complete reply.
func Parse(text string) (visible string, calls []tools.Call, warnings []string) {
	p := NewParser()
	visible = p.Feed(text)
	visible += p.Finish()
	return visible, p.Calls(), p.Warnings()
}
