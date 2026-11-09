package dsml

import (
	"fmt"
	"strings"
	"unicode/utf8"
	"unsafe"

	"kai/internal/tools"
)

// Parser reads a model reply as it streams in. Feed it text deltas: it hands
// back the text that is safe to show, holds back anything that might be the
// start of a tool-call marker, and collects the calls it finds.
type Parser struct {
	// raw holds the unread reply. It is append-only (bytes already in it are
	// never overwritten), so buf can be a zero-copy string view of it: adding
	// a chunk is amortised O(chunk) instead of copying the whole buffer, which
	// made a long file quadratic.
	raw     []byte
	buf     string
	inCalls bool
	calls   []tools.Call
	warns   []string
	seq     int

	// closeScan is how much of buf was already searched for the closing
	// wrapper tag. It keeps a long tool call (a big file) linear: each chunk
	// only scans the new bytes instead of the whole buffer again.
	closeScan int
	pr        progressState
}

func NewParser() *Parser { return &Parser{} }

// Feed adds a chunk and returns the visible text it releases.
func (p *Parser) Feed(chunk string) string {
	p.push(chunk)
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
//
// It is called for every chunk, so it works incrementally (see progressState)
// and costs time proportional to the new bytes, not to the file so far.
func (p *Parser) Progress() (ToolProgress, bool) {
	if !p.inCalls {
		return ToolProgress{}, false
	}
	return p.pr.update(p.buf)
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
			loc := p.findClose()
			if loc == nil {
				if final {
					p.addCalls(p.buf)
					p.warns = append(p.warns, "tool_calls block was never closed (the reply ended early)")
					p.drop(len(p.buf))
					p.inCalls = false
					p.resetScan()
				}
				break
			}
			p.addCalls(p.buf[:loc[0]])
			p.drop(loc[1])
			p.inCalls = false
			p.resetScan()
			continue
		}

		if loc := openRe.FindStringIndex(p.buf); loc != nil {
			out.WriteString(clean(p.buf[:loc[0]]))
			p.drop(loc[1])
			p.inCalls = true
			p.resetScan()
			continue
		}

		// An invoke with no wrapper around it: the call block starts here
		// and runs to the end of the reply.
		if loc := bareInvokeRe.FindStringIndex(p.buf); loc != nil {
			out.WriteString(clean(p.buf[:loc[0]]))
			p.drop(loc[0])
			p.inCalls = true
			p.resetScan()
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
		p.drop(cut)
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

// scanBackoff is how far a search restarts before the already-scanned point:
// a tag can arrive split across two chunks, and the longest one we look for
// is well under this.
const scanBackoff = 192

// resetScan forgets incremental state; call it whenever buf is re-sliced or
// the parser enters or leaves a tool_calls block.
func (p *Parser) resetScan() {
	p.closeScan = 0
	p.pr = progressState{}
}

// findClose looks for the closing wrapper tag, scanning only what has not
// been scanned before (plus a small overlap).
func (p *Parser) findClose() []int {
	from := p.closeScan - scanBackoff
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

// push appends a chunk to the buffer.
func (p *Parser) push(chunk string) {
	if chunk == "" {
		return
	}
	p.raw = append(p.raw, chunk...)
	p.view()
}

// drop discards the first n bytes of the buffer.
func (p *Parser) drop(n int) {
	if n >= len(p.raw) {
		p.raw = nil
	} else {
		p.raw = p.raw[n:]
	}
	p.view()
}

// view points buf at raw without copying. Strings taken from buf stay valid
// because raw is never written to except by appending past its end.
func (p *Parser) view() {
	if len(p.raw) == 0 {
		p.buf = ""
		return
	}
	p.buf = unsafe.String(&p.raw[0], len(p.raw))
}
