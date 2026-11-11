package components

import (
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleError
)

const (
	headNone = -1
	// stepStride packs (message, step) into one hover/click id.
	stepStride = 1 << 20

	// Bounds that keep the chat fast and the layout stable however long a
	// conversation, a thought, a file or a command output gets.
	liveThinkWindow = 6000 // bytes of a running thought that are laid out
	liveThinkRows   = 14   // rows of a running thought that are shown
	maxStoredOutput = 128 * 1024
)

type StepKind int

const (
	StepThinking StepKind = iota
	StepReadFile
	StepShell
	StepEdit
	StepWriting
	StepRunning // a command that has started and has not finished (live only)
)

type Step struct {
	Kind     StepKind
	Timeline Timeline
	Path     string
	Command  string
	Output   string
	Diff     []DiffLine
	Create   bool
}

type Message struct {
	Role    Role
	Text    string
	Steps   []Step
	Stopped bool
}

// textBuf is an append-only text. Appending a streamed token is amortised
// O(token); building a string and re-copying the text for every token made
// long answers and thoughts quadratic.
type textBuf struct{ b []byte }

func (t *textBuf) add(s string)     { t.b = append(t.b, s...) }
func (t textBuf) len() int          { return len(t.b) }
func (t textBuf) String() string    { return string(t.b) }
func (t textBuf) from(i int) string { return string(t.b[i:]) }

// cleanText drops control characters (escape sequences, carriage returns,
// NULs) that would move the cursor and corrupt the layout. Newlines and tabs
// stay. Most text has none, so the common case allocates nothing.
func cleanText(s string) string {
	bad := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			bad = true
			break
		}
	}
	if !bad {
		return s
	}
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

type liveStep struct {
	kind StepKind

	think     textBuf
	thinkDone bool
	took      time.Duration
	hidden    bool

	path string
	cmd  string
	out  string
	diff []DiffLine
	fail string // StepEdit: why the edit failed ("" when it worked)

	name   string
	create bool
	lines  int

	// rows is this step laid out at width rowsW; it is dropped whenever the
	// step changes, so a finished step is rendered once, not once per token.
	rows  []row
	rowsW int
	rowsK bool
}

func (s *liveStep) touch() { s.rows, s.rowsK = nil, false }

type liveReply struct {
	steps      []liveStep
	answer     textBuf
	answerDone bool
	answerGen  int
	stepVer    uint64 // changes whenever a step is added, changed or removed
}

var verCounter atomic.Uint64

func nextVer() uint64 { return verCounter.Add(1) }

type ChatModel struct {
	messages []Message
	width    int
	height   int
	offset   int // rows scrolled up from the bottom; 0 follows the newest row

	frame  int
	active bool
	live   liveReply

	hovering bool
	hover    int

	lastTotal int
	cache     *renderCache
}

// row is one screen line of the chat plus how the mouse can act on it.
type row struct {
	text  string
	hover string // text shown while the mouse is over a heading
	head  int
	headW int
	// render, when set, draws the row for the current animation frame (the
	// spinner); text is then only a fallback.
	render func(frame int, hover bool) string
}

// renderCache keeps every committed message laid out once, and the live
// reply laid out once per change. It is shared by copies of the ChatModel
// (the model is passed around by value), so the work is never repeated.
type renderCache struct {
	width   int
	msgRows [][]row
	flat    []row
	flatN   int
	flatOK  bool

	stepsOK   bool
	stepsVer  uint64
	stepsW    int
	stepsRows []row

	md mdCache
}

func (rc *renderCache) reset(width int) {
	*rc = renderCache{width: width}
}

func NewChat(w, h int) ChatModel {
	return ChatModel{width: w, height: h, cache: &renderCache{}}
}

func (c ChatModel) SetSize(w, h int) ChatModel {
	c.width, c.height = w, h
	return c.clamp()
}

func (c ChatModel) contentW() int {
	n := c.width - 2*Margin
	if n < 10 {
		n = 10
	}
	return n
}

func (c ChatModel) Clear() ChatModel {
	c.messages = nil
	c.offset = 0
	c.lastTotal = 0
	c.active = false
	c.hovering = false
	c.live = liveReply{}
	c.cache = &renderCache{}
	return c
}

func (c ChatModel) rc() *renderCache {
	if c.cache == nil {
		return &renderCache{}
	}
	return c.cache
}

func (c ChatModel) AppendUser(text string) ChatModel {
	c.messages = append(c.messages, Message{Role: RoleUser, Text: cleanText(text)})
	c.offset = 0
	return c
}

func (c ChatModel) AppendError(text string) ChatModel {
	c.messages = append(c.messages, Message{Role: RoleError, Text: cleanText(text)})
	c.offset = 0
	return c
}

func (c ChatModel) Active() bool { return c.active }

func (c ChatModel) Stop() ChatModel { return c.cut(true) }

func (c ChatModel) Fail(text string) ChatModel { return c.cut(false).AppendError(text) }

// capOutput bounds what is kept of a tool's output: the head and the tail,
// which is where the useful part of a long output usually is.
func capOutput(s string) string {
	if len(s) <= maxStoredOutput {
		return s
	}
	half := maxStoredOutput / 2
	head, tail := s[:half], s[len(s)-half:]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return head + "\n… " + itoa(len(s)-len(head)-len(tail)) + " bytes hidden …\n" + tail
}

func (c ChatModel) DemoteAnswer() ChatModel {
	if !c.active || c.live.answer.len() == 0 {
		return c
	}
	c.live.steps = append(c.live.steps, liveStep{kind: StepThinking, think: c.live.answer, thinkDone: true, took: time.Second})
	c.live.answer = textBuf{}
	c.live.answerDone = false
	c.live.answerGen++
	c.live.stepVer = nextVer()
	return c
}

// finish turns the live reply into a committed message.
func (c ChatModel) finish(stopped, dropEmpty bool) ChatModel {
	l := c.live
	msg := Message{Role: RoleAssistant, Stopped: stopped}
	for _, s := range l.steps {
		if s.kind == StepWriting || s.kind == StepRunning || (s.kind == StepThinking && s.think.len() == 0) {
			continue
		}
		if stopped && s.kind == StepThinking {
			s.hidden = false
		}
		msg.Steps = append(msg.Steps, s.toStep())
	}
	msg.Text = l.answer.String()
	if stopped || len(msg.Steps) > 0 || msg.Text != "" || !dropEmpty {
		c.messages = append(c.messages, msg)
	}
	c.active = false
	c.live = liveReply{}
	c.hovering = false
	return c
}

func (c ChatModel) cut(stopped bool) ChatModel {
	if !c.active {
		return c
	}
	c = c.finish(stopped, true)
	c.offset = 0
	return c
}

func stoppedLine(cw int) string {
	const label = " STOPPED "
	w := cw - 2
	dashes := w - len(label)
	if dashes < 4 {
		dashes = 4
	}
	left := dashes / 2
	return " " + LineStyle.Render(strings.Repeat("-", left)) +
		ErrorStyle.Bold(true).Render(label) +
		LineStyle.Render(strings.Repeat("-", dashes-left))
}

func (c ChatModel) StartReply() ChatModel {
	c.active = true
	c.live = liveReply{stepVer: nextVer()}
	c.frame = 0
	c.offset = 0
	return c
}

// last returns the newest live step, or nil.
func (c *ChatModel) last() *liveStep {
	if n := len(c.live.steps); n > 0 {
		return &c.live.steps[n-1]
	}
	return nil
}

// closeThought marks an unfinished thought as finished: the model moved on.
func (c *ChatModel) closeThought() {
	if s := c.last(); s != nil && s.kind == StepThinking && !s.thinkDone {
		s.thinkDone = true
		s.touch()
	}
}

func (c ChatModel) StreamThinking(s string) ChatModel {
	if s == "" {
		return c
	}
	st := c.last()
	if st == nil || st.kind != StepThinking || st.thinkDone {
		c.live.steps = append(c.live.steps, liveStep{kind: StepThinking})
		st = c.last()
	}
	st.think.add(cleanText(s))
	st.touch()
	c.live.stepVer = nextVer()
	return c
}

func (c ChatModel) EndThinking(took time.Duration) ChatModel {
	if s := c.last(); s != nil && s.kind == StepThinking && !s.thinkDone {
		s.thinkDone = true
		s.took = took
		s.hidden = true
		s.touch()
		c.live.stepVer = nextVer()
	}
	return c
}

func (c ChatModel) ReadFile(path string) ChatModel {
	c.closeThought()
	c.live.steps = append(c.live.steps, liveStep{kind: StepReadFile, path: path})
	c.live.stepVer = nextVer()
	return c
}

// ShellStart shows a command that is running, so a slow command is not a
// silent gap in the timeline. RunShell replaces it with the finished block.
func (c ChatModel) ShellStart(cmd string) ChatModel {
	c.closeThought()
	c = c.dropTransient()
	c.live.steps = append(c.live.steps, liveStep{kind: StepRunning, cmd: cmd})
	c.live.stepVer = nextVer()
	return c
}

func (c ChatModel) RunShell(cmd, output string) ChatModel {
	c.closeThought()
	c = c.dropTransient()
	c.live.steps = append(c.live.steps, liveStep{kind: StepShell, cmd: cmd, out: capOutput(output)})
	c.live.stepVer = nextVer()
	return c
}

// EditFile adds a finished file step to the timeline: the full content of a
// created file, or the diff of an edit. The node always starts collapsed (its
// heading only): a file or a diff can be long, and the conversation must stay
// readable. Clicking the heading opens it.
func (c ChatModel) EditFile(path string, create bool, diff []DiffLine) ChatModel {
	c.closeThought()
	c = c.dropTransient()
	c.live.steps = append(c.live.steps, liveStep{kind: StepEdit, path: path, diff: diff, create: create, hidden: true})
	c.live.stepVer = nextVer()
	return c
}

// EditFailed adds a file step that did not work, with a one-line reason.
func (c ChatModel) EditFailed(path string, create bool, reason string) ChatModel {
	c.closeThought()
	c = c.dropTransient()
	c.live.steps = append(c.live.steps, liveStep{kind: StepEdit, path: path, create: create, fail: oneLine(reason, 200)})
	c.live.stepVer = nextVer()
	return c
}

// WriteProgress shows the file the model is writing and how many lines have
// arrived. It updates one line in place instead of adding a line per update.
func (c ChatModel) WriteProgress(name, path string, lines int, creating bool) ChatModel {
	c.closeThought()
	if s := c.last(); s != nil && s.kind == StepWriting {
		if s.name == name && s.path == path && s.lines == lines && s.create == creating {
			return c
		}
		s.name, s.path, s.lines, s.create = name, path, lines, creating
		s.touch()
		c.live.stepVer = nextVer()
		return c
	}
	c.live.steps = append(c.live.steps, liveStep{kind: StepWriting, name: name, path: path, create: creating, lines: lines})
	c.live.stepVer = nextVer()
	return c
}

// dropTransient removes trailing "in progress" lines (writing, running).
func (c ChatModel) dropTransient() ChatModel {
	n := len(c.live.steps)
	for n > 0 && (c.live.steps[n-1].kind == StepWriting || c.live.steps[n-1].kind == StepRunning) {
		n--
	}
	if n != len(c.live.steps) {
		c.live.steps = c.live.steps[:n]
		c.live.stepVer = nextVer()
	}
	return c
}

func (c ChatModel) EndWrite() ChatModel { return c.dropTransient() }

func (c ChatModel) StreamAnswer(s string) ChatModel {
	if s == "" {
		return c
	}
	c.live.answer.add(cleanText(s))
	return c
}

func (c ChatModel) EndAnswer() ChatModel {
	c.live.answerDone = true
	return c
}

func (s liveStep) toStep() Step {
	switch s.kind {
	case StepReadFile:
		return Step{Kind: StepReadFile, Path: s.path}
	case StepShell:
		return Step{Kind: StepShell, Command: s.cmd, Output: s.out}
	case StepEdit:
		return Step{Kind: StepEdit, Path: s.path, Diff: s.diff, Create: s.create, Output: s.fail,
			Timeline: FileTimeline(s.path, s.create, s.diff, s.fail, s.hidden)}
	}
	tl := Timeline{Collapsed: s.hidden, Took: s.took}
	if s.think.len() > 0 {
		tl.Nodes = []TimelineNode{{Content: s.think.String()}}
	}
	return Step{Kind: StepThinking, Timeline: tl}
}

// Tick advances the animation and commits a finished reply.
func (c ChatModel) Tick() ChatModel {
	c.frame++
	if !c.active || !c.live.answerDone {
		return c
	}
	return c.finish(false, true)
}

// ---------------------------------------------------------------- layout

func (c ChatModel) totalRows() int {
	n := 0
	for _, seg := range c.segments() {
		n += len(seg)
	}
	return n
}

func (c ChatModel) maxOffset() int {
	n := c.totalRows() - c.height
	if n < 0 {
		return 0
	}
	return n
}

// clamp keeps the scroll position valid. While the reader is scrolled up,
// rows that arrive below keep the view where it was instead of dragging it.
func (c ChatModel) clamp() ChatModel {
	total := c.totalRows()
	if c.offset > 0 && c.lastTotal > 0 && total > c.lastTotal {
		c.offset += total - c.lastTotal
	}
	c.lastTotal = total
	if m := total - c.height; c.offset > m {
		c.offset = m
	}
	if c.offset < 0 {
		c.offset = 0
	}
	return c
}

func (c ChatModel) ScrollUp(n int) ChatModel {
	c.offset += n
	return c.clamp()
}

func (c ChatModel) ScrollDown(n int) ChatModel {
	c.offset -= n
	return c.clamp()
}

// ScrollToBottom jumps to the newest row.
func (c ChatModel) ScrollToBottom() ChatModel {
	c.offset = 0
	return c.clamp()
}

func answerLines(text string, cw int) []string {
	var out []string
	for _, l := range renderMarkdown(text, cw-3) {
		out = append(out, "   "+l)
	}
	return out
}

func readFileLine(path string) string {
	return " " + AccentStyle.Render("✱") + " " +
		AccentStyle.Render("Read file") + " " +
		DimStyle.Render("(") + MutedStyle.Render(path) + DimStyle.Render(")")
}

func writeLine(icon, path string, create bool, lines int) string {
	verb := "editing"
	if create {
		verb = "creating"
	}
	s := " " + AccentStyle.Render(icon) + " " +
		AccentStyle.Render(verb+" file") + " " +
		DimStyle.Render("(") + MutedStyle.Render(path) + DimStyle.Render(")")
	if lines > 0 {
		s += " " + DimStyle.Render("· "+itoa(lines)+" lines")
	}
	return s
}

func runLine(icon, cmd string) string {
	return " " + AccentStyle.Render(icon) + " " + AccentStyle.Render("running") + " " +
		DimStyle.Render("(") + MutedStyle.Render(oneLine(cmd, 80)) + DimStyle.Render(")")
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// plainRows turns lines into rows. A line holding a newline is split, so one
// row is always exactly one screen line.
func plainRows(lines []string) []row {
	rows := make([]row, 0, len(lines))
	for _, l := range lines {
		if strings.ContainsAny(l, "\n\r") {
			for _, p := range strings.Split(strings.ReplaceAll(l, "\r", ""), "\n") {
				rows = append(rows, row{text: p, head: headNone})
			}
			continue
		}
		rows = append(rows, row{text: l, head: headNone})
	}
	return rows
}

// fit cuts any row wider than the chat: a row that overflows would wrap in
// the terminal and push every line below it out of place.
func fit(rows []row, cw int) []row {
	for i := range rows {
		if rows[i].text != "" && ansi.StringWidth(rows[i].text) > cw {
			rows[i].text = ansi.Truncate(rows[i].text, cw, "")
		}
	}
	return rows
}

func timelineHead(tl Timeline, frame int, hover bool) string {
	head := AccentStyle.Render(tl.Heading())
	if hover && tl.Expandable() {
		head = HoverStyle.Render(tl.Heading())
	}
	return " " + AccentStyle.Render(tl.Icon(frame)) + " " + head
}

// timelineRows lays out a timeline. id is the hover/click id of its heading.
// Only a running thought is windowed (its tail is what matters while it
// streams); a finished one is shown whole.
func (c ChatModel) timelineRows(tl Timeline, cw, id int) []row {
	if tl.Running && !tl.Collapsed && len(tl.Nodes) == 1 {
		if t := tl.Nodes[0].Content; len(t) > liveThinkWindow {
			cut := len(t) - liveThinkWindow
			for cut < len(t) && !utf8.RuneStart(t[cut]) {
				cut++
			}
			tl.Nodes = []TimelineNode{{Content: t[cut:]}}
		}
	}
	lines := tl.Render(cw, 0, false)
	if tl.Running && len(lines) > 2+liveThinkRows {
		keep := lines[len(lines)-liveThinkRows:]
		lines = append([]string{lines[0], lines[1], " " + LineStyle.Render("│") + "  " + DimStyle.Render("…")}, keep...)
	}
	rows := plainRows(lines)
	if tl.Running || tl.Expandable() {
		tlc := tl
		rows[0].render = func(frame int, hover bool) string { return timelineHead(tlc, frame, hover) }
	}
	if tl.Expandable() {
		rows[0].head = id
		rows[0].headW = lipgloss.Width(timelineHead(tl, 0, false))
		rows[0].hover = timelineHead(tl, 0, true)
	}
	return rows
}

func (c ChatModel) renderMessage(i, cw int) []row {
	m := c.messages[i]
	var out []row
	switch m.Role {
	case RoleUser:
		out = append(out, plainRows(panelBlock(m.Text, cw, "", "", 0))...)
	case RoleAssistant:
		first := true
		for j, st := range m.Steps {
			if !first {
				out = append(out, row{head: headNone})
			}
			first = false
			switch st.Kind {
			case StepThinking:
				out = append(out, c.timelineRows(st.Timeline, cw, i*stepStride+j)...)
			case StepReadFile:
				out = append(out, plainRows([]string{readFileLine(st.Path)})...)
			case StepShell:
				out = append(out, plainRows(shellLines(st.Command, st.Output, cw))...)
			case StepEdit:
				tl := st.Timeline
				if tl.Title == "" { // saved by an older version: rebuild it
					tl = FileTimeline(st.Path, st.Create, st.Diff, st.Output, tl.Collapsed)
				}
				out = append(out, c.timelineRows(tl, cw, i*stepStride+j)...)
			}
		}
		if m.Text != "" {
			if !first {
				out = append(out, row{head: headNone})
			}
			out = append(out, plainRows(answerLines(m.Text, cw))...)
		}
		if m.Stopped {
			out = append(out, row{head: headNone})
			out = append(out, plainRows([]string{stoppedLine(cw)})...)
		}
	case RoleError:
		out = append(out, plainRows([]string{ErrorStyle.Render(" ✖ error")})...)
		for _, l := range wrapText(m.Text, cw-3) {
			out = append(out, plainRows([]string{"   " + ErrorStyle.Render(l)})...)
		}
	}
	out = append(out, row{head: headNone})
	return fit(out, cw)
}

// committedRows is the laid-out history. Each message is rendered once and
// kept; only new messages (or a toggled one) cost anything.
func (c ChatModel) committedRows() []row {
	cw := c.contentW()
	rc := c.rc()
	if rc.width != cw || len(rc.msgRows) > len(c.messages) {
		rc.reset(cw)
	}
	for len(rc.msgRows) < len(c.messages) {
		rc.msgRows = append(rc.msgRows, nil)
	}
	if !rc.flatOK {
		rc.flat = append(rc.flat[:0], row{head: headNone})
		rc.flatN = 0
		rc.flatOK = true
	}
	for ; rc.flatN < len(c.messages); rc.flatN++ {
		i := rc.flatN
		if rc.msgRows[i] == nil {
			rc.msgRows[i] = c.renderMessage(i, cw)
		}
		rc.flat = append(rc.flat, rc.msgRows[i]...)
	}
	return rc.flat
}

func (c ChatModel) liveStepRows(s *liveStep, j, cw int) []row {
	if s.rowsK && s.rowsW == cw {
		return s.rows
	}
	var rows []row
	switch s.kind {
	case StepThinking:
		tl := Timeline{Running: !s.thinkDone, Collapsed: s.hidden, Took: s.took}
		if s.think.len() > 0 {
			tl.Nodes = []TimelineNode{{Content: s.think.String()}}
		}
		rows = c.timelineRows(tl, cw, -(2 + j))
	case StepReadFile:
		rows = plainRows([]string{readFileLine(s.path)})
	case StepShell:
		rows = plainRows(shellLines(s.cmd, s.out, cw))
	case StepEdit:
		rows = c.timelineRows(FileTimeline(s.path, s.create, s.diff, s.fail, s.hidden), cw, -(2 + j))
	case StepWriting:
		path, create, lines := s.path, s.create, s.lines
		rows = []row{{head: headNone, text: writeLine("✎", path, create, lines)}}
		rows[0].render = func(frame int, _ bool) string {
			return writeLine(spinnerFrames[frame%len(spinnerFrames)], path, create, lines)
		}
	case StepRunning:
		cmd := s.cmd
		rows = []row{{head: headNone, text: runLine("✱", cmd)}}
		rows[0].render = func(frame int, _ bool) string {
			return runLine(spinnerFrames[frame%len(spinnerFrames)], cmd)
		}
	}
	rows = fit(rows, cw)
	s.rows, s.rowsW, s.rowsK = rows, cw, true
	return rows
}

var gapRow = []row{{head: headNone}}

// segments is everything on the chat, top to bottom, as a few slices that
// are cached separately: the history, the live steps, the lines of the
// answer that can no longer change, and the answer's unfinished tail. A new
// token only touches the tail, so streaming costs the same at any length.
func (c ChatModel) segments() [][]row {
	hist := c.committedRows()
	if !c.active {
		return [][]row{hist}
	}
	cw := c.contentW()
	rc := c.rc()
	if !rc.stepsOK || rc.stepsVer != c.live.stepVer || rc.stepsW != cw {
		var out []row
		for j := range c.live.steps {
			if j > 0 {
				out = append(out, row{head: headNone})
			}
			out = append(out, c.liveStepRows(&c.live.steps[j], j, cw)...)
		}
		rc.stepsOK, rc.stepsVer, rc.stepsW, rc.stepsRows = true, c.live.stepVer, cw, out
	}
	segs := [][]row{hist, rc.stepsRows}
	if c.live.answer.len() > 0 {
		stable, tail := rc.md.render(c.live.answer, c.live.answerGen, cw)
		if len(rc.stepsRows) > 0 {
			segs = append(segs, gapRow)
		}
		segs = append(segs, stable, tail)
	}
	return segs
}

// mdCache renders a streamed answer incrementally. Markdown is rendered line
// by line, so every complete line is rendered exactly once and kept; only the
// unfinished last line is redone for each token.
type mdCache struct {
	ok     bool
	width  int
	gen    int
	stable int // bytes of the answer (whole lines) already rendered
	state  mdState
	rows   []row
}

const answerPrefix = "   "

func isBlankAnswerRow(r row) bool { return r.text == answerPrefix }

// render returns the rows of the answer in two parts: the rows of complete
// lines (append-only, shared between calls) and the rows of the last,
// unfinished line. Trailing blank rows are left out, as for a finished answer.
func (m *mdCache) render(text textBuf, gen, cw int) (stable, tail []row) {
	w := cw - len(answerPrefix)
	if !m.ok || m.width != cw || m.gen != gen || m.stable > text.len() {
		*m = mdCache{ok: true, width: cw, gen: gen, state: newMdState()}
	}
	b := text.b
	end := 0
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			end = i + 1
			break
		}
	}
	if end > m.stable {
		rows, st := renderMarkdownState(string(b[m.stable:end-1]), w, m.state)
		for _, r := range rows {
			m.rows = append(m.rows, row{head: headNone, text: answerPrefix + r})
		}
		m.state, m.stable = st, end
	}
	tailText, _ := renderMarkdownState(string(b[m.stable:]), w, m.state)
	for len(tailText) > 0 && tailText[len(tailText)-1] == "" {
		tailText = tailText[:len(tailText)-1]
	}
	for _, r := range tailText {
		tail = append(tail, row{head: headNone, text: answerPrefix + r})
	}
	stable = m.rows
	if len(tail) == 0 {
		for len(stable) > 0 && isBlankAnswerRow(stable[len(stable)-1]) {
			stable = stable[:len(stable)-1]
		}
	}
	return stable, tail
}

// window returns the rows on screen without copying the whole chat.
func (c ChatModel) window() []row {
	segs := c.segments()
	total := 0
	for _, seg := range segs {
		total += len(seg)
	}
	h := c.height
	if h < 0 {
		h = 0
	}
	off := c.offset
	if off > total-h {
		off = total - h
	}
	if off < 0 {
		off = 0
	}
	end := total - off
	start := end - h
	if start < 0 {
		start = 0
	}
	out := make([]row, 0, end-start)
	base := 0
	for _, seg := range segs {
		lo, hi := start-base, end-base
		if lo < 0 {
			lo = 0
		}
		if hi > len(seg) {
			hi = len(seg)
		}
		if lo < hi {
			out = append(out, seg[lo:hi]...)
		}
		base += len(seg)
	}
	return out
}

func (c ChatModel) hit(x, y int) int {
	vis := c.window()
	if y < 0 || y >= len(vis) {
		return headNone
	}
	r := vis[y]
	if r.head == headNone || x < Margin || x >= Margin+r.headW {
		return headNone
	}
	return r.head
}

func (c ChatModel) Hover(x, y int) ChatModel {
	id := c.hit(x, y)
	c.hovering = id != headNone
	c.hover = id
	return c
}

func (c ChatModel) Click(x, y int) ChatModel {
	id := c.hit(x, y)
	if id == headNone {
		return c
	}
	rc := c.rc()
	if id <= -2 {
		j := -id - 2
		if j < len(c.live.steps) {
			s := &c.live.steps[j]
			s.hidden = !s.hidden
			s.touch()
			c.live.stepVer = nextVer()
		}
	} else {
		i, j := id/stepStride, id%stepStride
		if i < len(c.messages) && j < len(c.messages[i].Steps) {
			msgs := make([]Message, len(c.messages))
			copy(msgs, c.messages)
			steps := make([]Step, len(msgs[i].Steps))
			copy(steps, msgs[i].Steps)
			steps[j].Timeline.Collapsed = !steps[j].Timeline.Collapsed
			msgs[i].Steps = steps
			c.messages = msgs
			if i < len(rc.msgRows) {
				rc.msgRows[i] = nil
			}
			rc.flatOK = false
		}
	}
	c.hovering = true
	c.hover = id
	// A click reshapes the rows; do not treat that as new output below.
	c.lastTotal = 0
	return c.clamp()
}

// LastAnswer returns the text of the newest assistant message that has any.
func (c ChatModel) LastAnswer() string {
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Role == RoleAssistant && c.messages[i].Text != "" {
			return c.messages[i].Text
		}
	}
	return ""
}

func (c ChatModel) View() string {
	cw := c.contentW()
	rows := make([]string, 0, c.height)
	for _, r := range c.window() {
		text := r.text
		hov := c.hovering && r.head != headNone && r.head == c.hover
		switch {
		case r.render != nil:
			text = r.render(c.frame/3, hov)
			if ansi.StringWidth(text) > cw {
				text = ansi.Truncate(text, cw, "")
			}
		case hov && r.hover != "":
			text = r.hover
		}
		rows = append(rows, spaces(Margin)+padRight(text, cw)+spaces(c.width-cw-Margin))
	}
	for len(rows) < c.height {
		rows = append(rows, spaces(c.width))
	}
	return strings.Join(rows, "\n")
}
