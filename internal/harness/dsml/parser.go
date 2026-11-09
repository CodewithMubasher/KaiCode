package dsml

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"kai/internal/tools"
)

// Parser reads a model reply as it streams in. Feed it text deltas: it hands
// back the text that is safe to show, holds back anything that might be the
// start of a tool-call marker, and collects the calls it finds.
type Parser struct {
	buf     string
	inCalls bool
	calls   []tools.Call
	warns   []string
	seq     int
}

func NewParser() *Parser { return &Parser{} }

// Feed adds a chunk and returns the visible text it releases.
func (p *Parser) Feed(chunk string) string {
	p.buf += chunk
	return p.drain(false)
}

// Finish is called when the stream ends. It releases held-back text and
// salvages a tool-call block that was cut off.
func (p *Parser) Finish() string { return p.drain(true) }

// Calls returns the tool calls found so far.
func (p *Parser) Calls() []tools.Call { return p.calls }

// Warnings lists problems found while parsing (unclosed blocks, bad JSON).
func (p *Parser) Warnings() []string { return p.warns }

// InCalls reports whether the parser is inside a tool_calls block.
func (p *Parser) InCalls() bool { return p.inCalls }

// ToolProgress describes a tool call the model is still writing, so the UI can
// show it before the call is complete.
type ToolProgress struct {
	Name     string // tool name, e.g. "edit_file"
	Path     string // the path argument, once its tag has arrived
	Lines    int    // lines of new content received so far
	Creating bool   // no old_string (or an empty one): a new file
}

// Progress returns the call currently being written, if one is open. It is
// only meaningful while the reply is streaming: once the invoke is closed the
// call is complete and reported through Calls.
func (p *Parser) Progress() (ToolProgress, bool) {
	if !p.inCalls {
		return ToolProgress{}, false
	}
	locs := invokeOpenRe.FindAllStringSubmatchIndex(p.buf, -1)
	if len(locs) == 0 {
		return ToolProgress{}, false
	}
	last := locs[len(locs)-1]
	body := p.buf[last[1]:]
	if invokeCloseRe.MatchString(body) {
		return ToolProgress{}, false // the last call is complete
	}

	prog := ToolProgress{Name: p.buf[last[2]:last[3]], Creating: true}
	if v, closed, ok := partialParam(body, "path"); ok && closed {
		prog.Path = strings.TrimSpace(v)
	}
	if v, _, ok := partialParam(body, "new_string"); ok {
		prog.Lines = lineCount(v)
	} else if v, _, ok := partialParam(body, "content"); ok {
		prog.Lines = lineCount(v)
	}
	if old, _, ok := partialParam(body, "old_string"); ok {
		prog.Creating = strings.TrimSpace(old) == ""
	}
	if prog.Name == "" || prog.Path == "" {
		return ToolProgress{}, false
	}
	return prog, true
}

// partialParam returns the value of a parameter inside body and whether its
// closing tag has arrived; the value stops at that tag or at the end of body.
func partialParam(body, name string) (value string, closed, found bool) {
	for _, m := range paramOpenRe.FindAllStringSubmatchIndex(body, -1) {
		if body[m[2]:m[3]] != name {
			continue
		}
		rest := body[m[1]:]
		if loc := paramCloseRe.FindStringIndex(rest); loc != nil {
			return rest[:loc[0]], true, true
		}
		return rest, false, true
	}
	return "", false, false
}

// lineCount counts the lines in a parameter value; 0 while it is still empty.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func (p *Parser) nextID() string {
	p.seq++
	return fmt.Sprintf("call_%d", p.seq)
}

func (p *Parser) addCalls(body string) {
	calls, warns := parseInvokes(body, p.nextID)
	p.calls = append(p.calls, calls...)
	p.warns = append(p.warns, warns...)
	if len(calls) == 0 && strings.TrimSpace(body) != "" {
		p.warns = append(p.warns, "tool_calls block contained no complete invoke")
	}
}

func (p *Parser) drain(final bool) string {
	var out strings.Builder
	for {
		if p.inCalls {
			loc := closeRe.FindStringIndex(p.buf)
			if loc == nil {
				if final {
					p.addCalls(p.buf)
					p.warns = append(p.warns, "tool_calls block was never closed (the reply ended early)")
					p.buf, p.inCalls = "", false
				}
				break
			}
			p.addCalls(p.buf[:loc[0]])
			p.buf = p.buf[loc[1]:]
			p.inCalls = false
			continue
		}

		if loc := openRe.FindStringIndex(p.buf); loc != nil {
			out.WriteString(clean(p.buf[:loc[0]]))
			p.buf = p.buf[loc[1]:]
			p.inCalls = true
			continue
		}

		// An invoke with no wrapper around it: the call block starts here
		// and runs to the end of the reply.
		if loc := bareInvokeRe.FindStringIndex(p.buf); loc != nil {
			out.WriteString(clean(p.buf[:loc[0]]))
			p.buf = p.buf[loc[0]:]
			p.inCalls = true
			continue
		}

		// No marker yet. Release everything except a tail that could still
		// grow into one.
		keep := 0
		if !final {
			keep = partialTail(p.buf)
		}
		cut := len(p.buf) - keep
		out.WriteString(clean(p.buf[:cut]))
		p.buf = p.buf[cut:]
		break
	}
	return out.String()
}

// partialTail returns how many bytes at the end of s might be the start of a
// "<|DSML|..." marker (or a "<|end▁of▁sentence|>" token) and so must wait.
func partialTail(s string) int {
	i := strings.LastIndex(s, "<")
	if i < 0 {
		return 0
	}
	tail := s[i:]
	if len(tail) > 64 || strings.Contains(tail, ">") {
		return 0
	}
	if len(tail) > 1 {
		if !utf8.FullRuneInString(tail[1:]) {
			return len(tail) // half of a multi-byte character such as "｜": wait for the rest
		}
		r, _ := utf8.DecodeRuneInString(tail[1:])
		if r != '|' && r != '｜' {
			return 0
		}
	}
	return len(tail)
}

// Parse is the one-shot form: it returns the visible text and the calls of a
// complete reply.
func Parse(text string) (visible string, calls []tools.Call, warnings []string) {
	p := NewParser()
	visible = p.Feed(text)
	visible += p.Finish()
	return visible, p.Calls(), p.Warnings()
}
