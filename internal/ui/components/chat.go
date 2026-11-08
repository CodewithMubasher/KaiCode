package components

import (
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleError
)

const (
	typingCursor = "▍"
	readFileHold = 12 // ticks (~360ms) a "Read file" step stays current
	shellHold    = 10 // ticks (~300ms) a shell command "runs" before its output types
	editHold     = 8  // ticks before an edit starts drawing its diff
)

// Heading ids for hover/click hit-testing:
//
//	headNone        no heading
//	>= 0            message*stepStride + step (committed history)
//	<= -2           live reply step, index = -id-2
const (
	headNone   = -1
	stepStride = 1000
)

// StepKind is the type of one step inside an assistant reply.
type StepKind int

const (
	// StepThinking is a collapsible timeline (thought for Ns + nodes).
	StepThinking StepKind = iota
	// StepReadFile is a simple, non-collapsible "Read file (path)" line.
	StepReadFile
	// StepShell is a command panel: "$ cmd" plus its output.
	StepShell
	// StepEdit is a diff card: "← Edit path" plus changed lines.
	StepEdit
)

// Step is one entry in an assistant reply, shown in order above the answer.
type Step struct {
	Kind     StepKind
	Timeline Timeline // StepThinking
	Path     string   // StepReadFile
	Command  string   // StepShell
	Output   string   // StepShell
	Diff     []DiffLine // StepEdit
}

// Message is one chat entry. Assistant messages carry ordered Steps
// (thinking, read file, thinking, ...) and Text holds the final answer.
type Message struct {
	Role  Role
	Text  string
	Steps []Step
}

// liveStep is a step of the reply currently being streamed.
type liveStep struct {
	kind StepKind

	// thinking
	think     []rune
	shown     int
	thinkDone bool
	took      time.Duration
	hidden    bool // nodes hidden (auto after typing, or toggled by click)

	// read file
	path string
	age  int

	// shell (age and shown are shared with the kinds above)
	cmd string
	out []rune

	// edit (path is the file; shown counts diff lines drawn)
	diff []DiffLine

	finished bool
}

type liveReply struct {
	steps       []liveStep
	answer      []rune
	answerShown int
	answerDone  bool
}

type ChatModel struct {
	messages []Message
	width    int
	height   int
	offset   int // rows scrolled up from the bottom

	frame  int // animation frame, advanced by Tick
	active bool
	live   liveReply

	hovering bool // mouse is over a heading
	hover    int  // id of that heading
}

// row is one rendered line of the chat. head is the id of the timeline
// whose heading this row is (or headNone), headW the heading's width.
type row struct {
	text  string
	head  int
	headW int
}

func NewChat(w, h int) ChatModel {
	return ChatModel{width: w, height: h}
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
	c.active = false
	c.hovering = false
	c.live = liveReply{}
	return c
}

func (c ChatModel) AppendUser(text string) ChatModel {
	c.messages = append(c.messages, Message{Role: RoleUser, Text: text})
	c.offset = 0
	return c
}

func (c ChatModel) AppendError(text string) ChatModel {
	c.messages = append(c.messages, Message{Role: RoleError, Text: text})
	c.offset = 0
	return c
}

// Active reports whether a reply is still being streamed/typed.
func (c ChatModel) Active() bool { return c.active }

func (c ChatModel) cloneSteps() []liveStep {
	steps := make([]liveStep, len(c.live.steps))
	copy(steps, c.live.steps)
	return steps
}

// StartReply begins a new streamed reply. A thinking step starts spinning
// immediately.
func (c ChatModel) StartReply() ChatModel {
	c.active = true
	c.live = liveReply{steps: []liveStep{{kind: StepThinking}}}
	c.frame = 0
	c.offset = 0
	return c
}

// StreamThinking appends thinking text to the current thinking step (or
// starts a new one after a Read file step / finished thinking).
func (c ChatModel) StreamThinking(s string) ChatModel {
	steps := c.cloneSteps()
	n := len(steps)
	if n == 0 || steps[n-1].kind != StepThinking || steps[n-1].thinkDone {
		steps = append(steps, liveStep{kind: StepThinking})
		n++
	}
	steps[n-1].think = append(steps[n-1].think, []rune(s)...)
	c.live.steps = steps
	return c
}

// EndThinking marks the current thinking step as complete. Once its text
// has been typed it stops spinning and collapses to its heading.
func (c ChatModel) EndThinking(took time.Duration) ChatModel {
	steps := c.cloneSteps()
	n := len(steps)
	if n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
		steps[n-1].took = took
	}
	c.live.steps = steps
	return c
}

// ReadFile adds a simple (non-collapsible) "Read file (path)" step.
func (c ChatModel) ReadFile(path string) ChatModel {
	steps := c.cloneSteps()
	if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
	}
	steps = append(steps, liveStep{kind: StepReadFile, path: path})
	c.live.steps = steps
	return c
}

// RunShell adds a shell step: the command shows at once, then its output
// types in after a short "running" pause.
func (c ChatModel) RunShell(cmd, output string) ChatModel {
	steps := c.cloneSteps()
	if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
	}
	steps = append(steps, liveStep{kind: StepShell, cmd: cmd, out: []rune(output)})
	c.live.steps = steps
	return c
}

// EditFile adds an edit step: the header shows at once, then the diff
// lines are drawn one by one.
func (c ChatModel) EditFile(path string, diff []DiffLine) ChatModel {
	steps := c.cloneSteps()
	if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
	}
	steps = append(steps, liveStep{kind: StepEdit, path: path, diff: diff})
	c.live.steps = steps
	return c
}

// StreamAnswer appends final-answer text. It starts typing after every
// step has finished.
func (c ChatModel) StreamAnswer(s string) ChatModel {
	c.live.answer = append(c.live.answer, []rune(s)...)
	return c
}

// EndAnswer marks the answer as complete; the reply is committed to the
// history once everything has been typed.
func (c ChatModel) EndAnswer() ChatModel {
	c.live.answerDone = true
	return c
}

// reveal returns how many runes should be visible after one tick. It types
// slowly when caught up and speeds up when there is a backlog.
func reveal(shown, total int) int {
	backlog := total - shown
	if backlog <= 0 {
		return shown
	}
	step := 2 + backlog/15
	if step > backlog {
		step = backlog
	}
	return shown + step
}

func firstUnfinished(steps []liveStep) int {
	for i, s := range steps {
		if !s.finished {
			return i
		}
	}
	return -1
}

func (s liveStep) toStep() Step {
	switch s.kind {
	case StepReadFile:
		return Step{Kind: StepReadFile, Path: s.path}
	case StepShell:
		return Step{Kind: StepShell, Command: s.cmd, Output: string(s.out)}
	case StepEdit:
		return Step{Kind: StepEdit, Path: s.path, Diff: s.diff}
	}
	tl := Timeline{Collapsed: s.hidden, Took: s.took}
	if len(s.think) > 0 {
		tl.Nodes = []TimelineNode{{Content: string(s.think)}}
	}
	return Step{Kind: StepThinking, Timeline: tl}
}

// Tick advances animation and typing by one frame (call every ~30ms).
// Steps are processed strictly in order; the answer types last.
func (c ChatModel) Tick() ChatModel {
	c.frame++
	if !c.active {
		return c
	}

	if idx := firstUnfinished(c.live.steps); idx >= 0 {
		steps := c.cloneSteps()
		s := &steps[idx]
		switch s.kind {
		case StepThinking:
			s.shown = reveal(s.shown, len(s.think))
			if s.thinkDone && s.shown >= len(s.think) {
				s.finished = true
				s.hidden = true // auto-collapse to the heading
			}
		case StepReadFile:
			s.age++
			if s.age >= readFileHold {
				s.finished = true
			}
		case StepShell:
			s.age++
			if s.age >= shellHold {
				s.shown = reveal(s.shown, len(s.out))
				if s.shown >= len(s.out) {
					s.finished = true
				}
			}
		case StepEdit:
			s.age++
			if s.age >= editHold {
				s.shown = (s.age-editHold)/2 + 1 // one diff line every 2 ticks
				if s.shown >= len(s.diff) {
					s.shown = len(s.diff)
					s.finished = true
				}
			}
		}
		c.live.steps = steps
		return c
	}

	l := c.live
	l.answerShown = reveal(l.answerShown, len(l.answer))
	if l.answerDone && l.answerShown >= len(l.answer) {
		msg := Message{Role: RoleAssistant, Text: string(l.answer)}
		for _, s := range l.steps {
			msg.Steps = append(msg.Steps, s.toStep())
		}
		c.messages = append(c.messages, msg)
		c.active = false
		c.live = liveReply{}
		return c
	}
	c.live = l
	return c
}

func (c ChatModel) maxOffset() int {
	n := len(c.lines()) - c.height
	if n < 0 {
		return 0
	}
	return n
}

func (c ChatModel) clamp() ChatModel {
	if m := c.maxOffset(); c.offset > m {
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

// answerLines renders final answer text at full brightness.
func answerLines(text string, cw int) []string {
	var out []string
	for _, l := range renderMarkdown(text, cw-3) {
		out = append(out, "   "+l)
	}
	return out
}

// readFileLine renders: ✱ Read file (app.tsx)
func readFileLine(path string) string {
	return " " + AccentStyle.Render("✱") + " " +
		AccentStyle.Render("Read file") + " " +
		DimStyle.Render("(") + MutedStyle.Render(path) + DimStyle.Render(")")
}

func plainRows(lines []string) []row {
	rows := make([]row, len(lines))
	for i, l := range lines {
		rows[i] = row{text: l, head: headNone}
	}
	return rows
}

// timelineRows renders a timeline; its heading row becomes clickable when
// the timeline has nodes.
func (c ChatModel) timelineRows(tl Timeline, cw, spin, id int) []row {
	hover := c.hovering && c.hover == id
	lines := tl.Render(cw, spin, hover)
	rows := plainRows(lines)
	if tl.Expandable() {
		rows[0].head = id
		rows[0].headW = lipgloss.Width(lines[0])
	}
	return rows
}

func (c ChatModel) lines() []row {
	cw := c.contentW()
	spin := c.frame / 3 // spinner steps every ~100ms
	blank := plainRows([]string{""})
	out := plainRows([]string{""})

	for i, m := range c.messages {
		switch m.Role {
		case RoleUser:
			out = append(out, plainRows(panelBlock(m.Text, cw, "", "", 0))...)
		case RoleAssistant:
			for j, st := range m.Steps {
				if j > 0 {
					out = append(out, blank...) // gap between steps
				}
				switch st.Kind {
				case StepThinking:
					out = append(out, c.timelineRows(st.Timeline, cw, spin, i*stepStride+j)...)
				case StepReadFile:
					out = append(out, plainRows([]string{readFileLine(st.Path)})...)
				case StepShell:
					out = append(out, plainRows(shellLines(st.Command, st.Output, cw))...)
				case StepEdit:
					out = append(out, plainRows(editLines(st.Path, st.Diff, cw))...)
				}
			}
			if m.Text != "" {
				out = append(out, blank...)
				out = append(out, plainRows(answerLines(m.Text, cw))...)
			}
		case RoleError:
			out = append(out, plainRows([]string{ErrorStyle.Render(" ✖ error")})...)
			for _, l := range wrapText(m.Text, cw-3) {
				out = append(out, plainRows([]string{"   " + ErrorStyle.Render(l)})...)
			}
		}
		out = append(out, blank...)
	}

	if c.active {
		l := c.live
		for j, s := range l.steps {
			if j > 0 {
				out = append(out, blank...) // gap between steps
			}
			switch s.kind {
			case StepThinking:
				tl := Timeline{Running: !s.finished, Collapsed: s.hidden, Took: s.took}
				switch {
				case !s.finished && s.shown > 0:
					tl.Nodes = []TimelineNode{{Content: string(s.think[:s.shown]) + typingCursor}}
				case s.finished && len(s.think) > 0:
					tl.Nodes = []TimelineNode{{Content: string(s.think)}}
				}
				out = append(out, c.timelineRows(tl, cw, spin, -(2+j))...)
			case StepReadFile:
				out = append(out, plainRows([]string{readFileLine(s.path)})...)
			case StepShell:
				text := string(s.out[:s.shown])
				if !s.finished && s.age >= shellHold {
					text += typingCursor
				}
				out = append(out, plainRows(shellLines(s.cmd, text, cw))...)
			case StepEdit:
				out = append(out, plainRows(editLines(s.path, s.diff[:s.shown], cw))...)
			}
			if !s.finished {
				break // later steps appear once this one is done
			}
		}
		if firstUnfinished(l.steps) < 0 && (len(l.answer) > 0 || l.answerDone) {
			out = append(out, blank...)
			out = append(out, plainRows(answerLines(string(l.answer[:l.answerShown])+typingCursor, cw))...)
		}
	}
	return out
}

// visible returns the rows currently on screen (the bottom window of the
// history, shifted up by the scroll offset).
func (c ChatModel) visible() []row {
	all := c.lines()
	h := c.height
	if len(all) <= h {
		return all
	}
	off := c.offset
	if off > len(all)-h {
		off = len(all) - h
	}
	end := len(all) - off
	return all[end-h : end]
}

// hit returns the timeline heading id under screen cell (x, y), or headNone.
func (c ChatModel) hit(x, y int) int {
	vis := c.visible()
	if y < 0 || y >= len(vis) {
		return headNone
	}
	r := vis[y]
	if r.head == headNone || x < Margin || x >= Margin+r.headW {
		return headNone
	}
	return r.head
}

// Hover updates which timeline heading (if any) the mouse is over.
func (c ChatModel) Hover(x, y int) ChatModel {
	id := c.hit(x, y)
	c.hovering = id != headNone
	c.hover = id
	return c
}

// Click toggles the thinking timeline whose heading is at (x, y), if any.
func (c ChatModel) Click(x, y int) ChatModel {
	id := c.hit(x, y)
	if id == headNone {
		return c
	}
	if id <= -2 {
		j := -id - 2
		steps := c.cloneSteps()
		if j < len(steps) {
			steps[j].hidden = !steps[j].hidden
		}
		c.live.steps = steps
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
		}
	}
	c.hovering = true
	c.hover = id
	return c.clamp()
}

// words counts whitespace-separated words in s.
func words(s string) int { return len(strings.Fields(s)) }

// wordCounts counts the words in the conversation as it stands right now,
// including the part of the live reply typed so far. "in" is what is fed to
// the model (user messages, command output); "out" is what the model wrote
// (thinking, tool calls, edits, answers). The whole history is counted, so a
// second turn is everything before it plus the new messages.
func (c ChatModel) wordCounts() (in, out int) {
	for _, m := range c.messages {
		switch m.Role {
		case RoleUser:
			in += words(m.Text)
		case RoleAssistant:
			out += words(m.Text)
		}
		for _, st := range m.Steps {
			switch st.Kind {
			case StepThinking:
				for _, nd := range st.Timeline.Nodes {
					out += words(nd.Content)
				}
			case StepReadFile:
				out += words("Read file " + st.Path)
			case StepShell:
				out += words(st.Command)
				in += words(st.Output)
			case StepEdit:
				out += words(st.Path) + diffWords(st.Diff)
			}
		}
	}
	if c.active {
		for _, s := range c.live.steps {
			switch s.kind {
			case StepThinking:
				out += words(string(s.think[:s.shown]))
			case StepReadFile:
				out += words("Read file " + s.path)
			case StepShell:
				out += words(s.cmd)
				in += words(string(s.out[:s.shown]))
			case StepEdit:
				out += words(s.path) + diffWords(s.diff[:s.shown])
			}
		}
		out += words(string(c.live.answer[:c.live.answerShown]))
	}
	return in, out
}

func diffWords(d []DiffLine) int {
	n := 0
	for _, l := range d {
		n += words(l.Text)
	}
	return n
}

// tokensPerWord is the rough conversion used for the context estimate.
const tokensPerWord = 1.3

// TokenCounts estimates input and output tokens: words * 1.3, rounded.
// The context used is their sum.
func (c ChatModel) TokenCounts() (in, out int) {
	wi, wo := c.wordCounts()
	return int(math.Round(float64(wi) * tokensPerWord)), int(math.Round(float64(wo) * tokensPerWord))
}

// LastAnswer returns the text of the last assistant reply ("" if none).
func (c ChatModel) LastAnswer() string {
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Role == RoleAssistant {
			return c.messages[i].Text
		}
	}
	return ""
}

func (c ChatModel) View() string {
	cw := c.contentW()
	rows := make([]string, 0, c.height)
	for _, r := range c.visible() {
		rows = append(rows, spaces(Margin)+padRight(r.text, cw)+spaces(c.width-cw-Margin))
	}
	for len(rows) < c.height {
		rows = append(rows, spaces(c.width))
	}
	return strings.Join(rows, "\n")
}
