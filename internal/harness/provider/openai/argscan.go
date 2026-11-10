package openai

import (
	"strings"
	"unicode/utf8"

	"kai/internal/harness"
)

// argScanner reads the JSON arguments of an edit_file call while they are
// still being streamed, one fragment at a time, and reports what the UI shows
// as live progress: the path, how many lines of new content have arrived, and
// whether the call creates a file (old_string empty or absent).
//
// It is incremental on purpose. Every byte is looked at once, so the total
// work is linear in the size of the call; re-parsing the text so far for each
// fragment would be quadratic and freeze the UI on a long file.
//
// It only understands what it needs: a flat object of "key": value pairs. If
// the text is not like that it stops reporting and does no harm; the complete
// arguments are parsed properly by the agent afterwards.
type argScanner struct {
	state int
	cur   string // key of the value being read

	key  strings.Builder // key being read
	path strings.Builder // decoded value of "path"

	esc    bool // after a backslash inside a string
	uni    int  // hex digits still expected in a \uXXXX escape
	uniVal rune
	depth  int  // nesting inside an array or object value
	inStr  bool // inside a string within such a value

	pathDone bool
	hasNew   bool // some of new_string (or content) has arrived
	nl       int  // newlines in it
	oldAny   bool // old_string holds something other than white space
	dead     bool

	scratch [4]byte
}

const (
	asStart  = iota
	asKey    // between pairs: waiting for a key or the end
	asKeyStr // inside a key
	asColon  // after a key
	asValue  // before a value
	asValStr // inside a string value
	asScalar // inside a number, true, false or null
	asNested // inside an array or object value
	asAfter  // after a value
	asEnd    // after the closing brace
)

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// str feeds one byte of a JSON string and returns the decoded bytes (possibly
// none) and whether the string ended.
func (s *argScanner) str(c byte) ([]byte, bool) {
	if s.uni > 0 {
		var v rune
		switch {
		case c >= '0' && c <= '9':
			v = rune(c - '0')
		case c >= 'a' && c <= 'f':
			v = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = rune(c-'A') + 10
		default:
			s.uni = 0
			return nil, false
		}
		s.uniVal = s.uniVal<<4 | v
		if s.uni--; s.uni == 0 {
			n := utf8.EncodeRune(s.scratch[:], s.uniVal)
			return s.scratch[:n], false
		}
		return nil, false
	}
	if s.esc {
		s.esc = false
		switch c {
		case 'n':
			return []byte{'\n'}, false
		case 't':
			return []byte{'\t'}, false
		case 'r':
			return []byte{'\r'}, false
		case 'b', 'f':
			return nil, false
		case 'u':
			s.uni, s.uniVal = 4, 0
			return nil, false
		}
		s.scratch[0] = c // \" \\ \/
		return s.scratch[:1], false
	}
	switch c {
	case '\\':
		s.esc = true
		return nil, false
	case '"':
		return nil, true
	}
	s.scratch[0] = c
	return s.scratch[:1], false
}

// Feed adds the next fragment of the arguments. ok is false until the path
// has fully arrived, as the UI needs it to name the file.
func (s *argScanner) Feed(frag string) (harness.ToolProgress, bool) {
	for i := 0; i < len(frag) && !s.dead; i++ {
		c := frag[i]
		switch s.state {
		case asStart:
			if c == '{' {
				s.state = asKey
			} else if !isSpace(c) {
				s.dead = true
			}
		case asKey:
			if c == '"' {
				s.key.Reset()
				s.state = asKeyStr
			} else if c == '}' {
				s.state = asEnd
			}
		case asKeyStr:
			b, end := s.str(c)
			s.key.Write(b)
			if end {
				s.cur = s.key.String()
				s.state = asColon
			}
		case asColon:
			if c == ':' {
				s.state = asValue
			} else if !isSpace(c) {
				s.dead = true
			}
		case asValue:
			switch {
			case isSpace(c):
			case c == '"':
				s.state = asValStr
			case c == '{' || c == '[':
				s.depth, s.inStr, s.state = 1, false, asNested
			default:
				s.state = asScalar
			}
		case asValStr:
			b, end := s.str(c)
			s.value(b)
			if end {
				if s.cur == "path" {
					s.pathDone = true
				}
				s.state = asAfter
			}
		case asScalar:
			switch c {
			case ',':
				s.state = asKey
			case '}':
				s.state = asEnd
			}
		case asNested:
			s.nested(c)
		case asAfter:
			switch c {
			case ',':
				s.state = asKey
			case '}':
				s.state = asEnd
			}
		}
	}
	return s.progress()
}

// value takes decoded bytes of the current string value.
func (s *argScanner) value(b []byte) {
	if len(b) == 0 {
		return
	}
	switch s.cur {
	case "path":
		s.path.Write(b)
	case "new_string", "content":
		s.hasNew = true
		for _, c := range b {
			if c == '\n' {
				s.nl++
			}
		}
	case "old_string":
		if strings.TrimSpace(string(b)) != "" {
			s.oldAny = true
		}
	}
}

// nested skips an array or object value, which edit_file does not use.
func (s *argScanner) nested(c byte) {
	if s.inStr {
		switch {
		case s.esc:
			s.esc = false
		case c == '\\':
			s.esc = true
		case c == '"':
			s.inStr = false
		}
		return
	}
	switch c {
	case '"':
		s.inStr = true
	case '{', '[':
		s.depth++
	case '}', ']':
		if s.depth--; s.depth == 0 {
			s.state = asAfter
		}
	}
}

func (s *argScanner) progress() (harness.ToolProgress, bool) {
	if s.dead || !s.pathDone {
		return harness.ToolProgress{}, false
	}
	lines := 0
	if s.hasNew {
		lines = s.nl + 1
	}
	return harness.ToolProgress{
		Name:     "edit_file",
		Path:     strings.TrimSpace(s.path.String()),
		Lines:    lines,
		Creating: !s.oldAny,
	}, true
}
