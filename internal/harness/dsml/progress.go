package dsml

import "strings"

// progressState tracks the tool call that is still being written, one chunk
// at a time. The old implementation re-ran several regular expressions over
// the whole buffer for every chunk, which is quadratic: creating a long file
// froze the UI. Here every search resumes where the last one stopped, so the
// total work is linear in the size of the reply.
//
// Offsets are absolute positions in Parser.buf. The state is valid only while
// buf is append-only; Parser.resetScan clears it when buf is re-sliced.
type progressState struct {
	openScan int // next position to look for an invoke open tag
	invOpen  int // start of the last invoke open tag (valid if haveInv)
	invBody  int // first byte after that tag
	name     string
	haveInv  bool

	closeScan int  // how far the invoke close tag was searched
	closed    bool // the invoke is complete

	paramScan int
	params    []paramState
}

type paramState struct {
	name     string
	valStart int
	valEnd   int // -1 while the closing tag has not arrived
	scan     int // how far the closing tag was searched
	nl       int // newlines counted so far in the value
	nlPos    int // how far newlines were counted
}

func clampFrom(n, floor int) int {
	if n < floor {
		return floor
	}
	return n
}

// update advances the state over buf and reports the call being written.
func (s *progressState) update(buf string) (ToolProgress, bool) {
	// 1. Find the most recent invoke open tag. Searches never start before
	// the end of the last tag accepted, so the overlap cannot re-find it.
	lastEnd := 0
	if s.haveInv {
		lastEnd = s.invBody
	}
	for {
		from := clampFrom(s.openScan-scanBackoff, lastEnd)
		m := invokeOpenRe.FindStringSubmatchIndex(buf[from:])
		if m == nil {
			s.openScan = len(buf)
			break
		}
		start, end := m[0]+from, m[1]+from
		s.haveInv, s.invOpen, s.invBody = true, start, end
		s.name = buf[m[2]+from : m[3]+from]
		s.openScan, lastEnd = end, end
		s.closed, s.closeScan = false, end
		s.paramScan, s.params = end, nil
	}
	if !s.haveInv {
		return ToolProgress{}, false
	}

	// 2. Has the invoke been closed? Then the call is complete.
	if !s.closed {
		from := clampFrom(s.closeScan-scanBackoff, s.invBody)
		if invokeCloseRe.MatchString(buf[from:]) {
			s.closed = true
		} else {
			s.closeScan = len(buf)
		}
	}
	if s.closed {
		return ToolProgress{}, false
	}

	// 3. Discover parameter open tags that arrived since last time.
	pEnd := s.invBody
	if n := len(s.params); n > 0 {
		pEnd = s.params[n-1].valStart
	}
	for {
		from := clampFrom(s.paramScan-scanBackoff, pEnd)
		m := paramOpenRe.FindStringSubmatchIndex(buf[from:])
		if m == nil {
			s.paramScan = len(buf)
			break
		}
		end := m[1] + from
		s.params = append(s.params, paramState{
			name: buf[m[2]+from : m[3]+from], valStart: end, valEnd: -1, scan: end, nlPos: end,
		})
		s.paramScan, pEnd = end, end
	}

	prog := ToolProgress{Name: s.name, Creating: true}
	if pp := s.find("path"); pp != nil {
		pp.advance(buf)
		if pp.valEnd >= 0 {
			prog.Path = strings.TrimSpace(buf[pp.valStart:pp.valEnd])
		}
	}
	if pp := s.find("new_string"); pp != nil {
		prog.Lines = pp.lines(buf)
	} else if pp := s.find("content"); pp != nil {
		prog.Lines = pp.lines(buf)
	}
	if pp := s.find("old_string"); pp != nil {
		pp.advance(buf)
		end := len(buf)
		if pp.valEnd >= 0 {
			end = pp.valEnd
		}
		prog.Creating = strings.TrimSpace(buf[pp.valStart:end]) == ""
	}
	if prog.Name == "" || prog.Path == "" {
		return ToolProgress{}, false
	}
	return prog, true
}

// find returns the first parameter with the given name.
func (s *progressState) find(name string) *paramState {
	for i := range s.params {
		if s.params[i].name == name {
			return &s.params[i]
		}
	}
	return nil
}

// advance looks for the parameter's closing tag in the new bytes only.
func (pp *paramState) advance(buf string) {
	if pp.valEnd >= 0 {
		return
	}
	from := clampFrom(pp.scan-scanBackoff, pp.valStart)
	if loc := paramCloseRe.FindStringIndex(buf[from:]); loc != nil {
		pp.valEnd = loc[0] + from
		return
	}
	pp.scan = len(buf)
}

// lines counts the lines of the value so far, adding only the new bytes.
func (pp *paramState) lines(buf string) int {
	pp.advance(buf)
	end := len(buf)
	if pp.valEnd >= 0 {
		end = pp.valEnd
	}
	if end > pp.nlPos {
		pp.nl += strings.Count(buf[pp.nlPos:end], "\n")
		pp.nlPos = end
	}
	if end == pp.valStart {
		return 0
	}
	return pp.nl + 1
}
