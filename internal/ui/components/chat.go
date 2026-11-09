package components

import (
	"fmt"
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
	// StepWriting is live-only: a tool call is still arriving. It is replaced
	// by the real step when the call is complete, never kept in history.
	StepWriting
)

// Step is one entry in an assistant reply, shown in order above the answer.
type Step struct {
	Kind     StepKind
	Timeline Timeline // StepThinking
	Path     string   // StepReadFile, StepEdit
	Command  string   // StepShell
	Output   string   // StepShell
	Diff     []DiffLine // StepEdit
	Create   bool       // StepEdit: a new file rather than a change
}

// Message is one chat entry. Assistant messages carry ordered Steps
// (thinking, read file, thinking, ...) and Text holds the final answer.
type Message struct {
	Role    Role
	Text    string
	Steps   []Step
	Stopped bool // the user pressed esc mid-reply; a STOPPED marker follows it
}

// liveStep is a step of the reply currently being streamed. Everything the
// provider sends is shown at once; only the spinner animates.
type liveStep struct {
	kind StepKind

	// thinking
	think     []rune
	thinkDone bool
	took      time.Duration
	hidden    bool // nodes hidden (collapsed when thinking ends, or by click)

	// read file
	path string

	// shell
	cmd string
	out []rune

	// edit
	diff []DiffLine

	// writing (a tool call is still arriving; path/name/lines update live)
	name   string
	lines  int
	create bool
}

type liveReply struct {
	steps      []liveStep
	answer     []rune
	answerDone bool
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

// Active reports whether a reply is still being streamed.
func (c ChatModel) Active() bool { return c.active }

func (c ChatModel) cloneSteps() []liveStep {
	steps := make([]liveStep, len(c.live.steps))
	copy(steps, c.live.steps)
	return steps
}

// Stop cuts the running reply short (esc). Whatever has arrived is kept as a
// normal message and flagged Stopped, so the chat ends it with a STOPPED
// marker.
func (c ChatModel) Stop() ChatModel { return c.cut(true) }

// Fail ends the running reply the same way, without the STOPPED marker, and
// adds an error row under it.
func (c ChatModel) Fail(text string) ChatModel { return c.cut(false).AppendError(text) }

// DemoteAnswer turns the answer text received so far into a finished thought.
// The harness calls it when a tool call follows some text: that text was the
// model thinking out loud, not the final answer.
func (c ChatModel) DemoteAnswer() ChatModel {
	if !c.active || len(c.live.answer) == 0 {
		return c
	}
	steps := c.cloneSteps()
	steps = append(steps, liveStep{kind: StepThinking, think: c.live.answer, thinkDone: true, took: time.Second})
	c.live.steps = steps
	c.live.answer = nil
	c.live.answerDone = false
	return c
}

func (c ChatModel) cut(stopped bool) ChatModel {
	if !c.active {
		return c
	}
	l := c.live
	msg := Message{Role: RoleAssistant, Stopped: stopped}
	for _, s := range l.steps {
		if s.kind == StepWriting || (s.kind == StepThinking && len(s.think) == 0) {
			continue // nothing of this step was ever on screen
		}
		if s.kind == StepThinking {
			s.hidden = false // keep the cut-off thought visible
		}
		msg.Steps = append(msg.Steps, s.toStep())
	}
	msg.Text = string(l.answer)
	if stopped || len(msg.Steps) > 0 || msg.Text != "" {
		c.messages = append(c.messages, msg)
	}
	c.active = false
	c.live = liveReply{}
	c.hovering = false
	c.offset = 0
	return c
}

// stoppedLine is the "---- STOPPED ----" marker, centred in cw cells.
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

// StartReply begins a new streamed reply. No step is created yet: the thinking
// timeline only appears once the model actually sends reasoning, so replies
// without it do not show an empty "thought" line.
func (c ChatModel) StartReply() ChatModel {
	c.active = true
	c.live = liveReply{}
	c.frame = 0
	c.offset = 0
	return c
}

// StreamThinking appends thinking text to the current thinking step (or
// starts a new one after a Read file step / finished thinking).
func (c ChatModel) StreamThinking(s string) ChatModel {
	if s == "" {
		return c
	}
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

// EndThinking marks the current thinking step as complete: it stops spinning
// and collapses to its heading.
func (c ChatModel) EndThinking(took time.Duration) ChatModel {
	steps := c.cloneSteps()
	n := len(steps)
	if n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
		steps[n-1].took = took
		steps[n-1].hidden = true
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

// RunShell adds a shell step: the command and its output show at once.
func (c ChatModel) RunShell(cmd, output string) ChatModel {
	steps := c.cloneSteps()
	if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
	}
	steps = append(steps, liveStep{kind: StepShell, cmd: cmd, out: []rune(output)})
	c.live.steps = steps
	return c
}

// EditFile adds an edit step with its full diff. create marks a brand-new
// file.
func (c ChatModel) EditFile(path string, create bool, diff []DiffLine) ChatModel {
	steps := c.cloneSteps()
	if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
	}
	steps = append(steps, liveStep{kind: StepEdit, path: path, diff: diff, create: create})
	c.live.steps = steps
	return c
}

// WriteProgress shows a tool call while the model is still writing it: a
// "creating/editing path" line whose line count grows with the reply. The
// step is replaced by the real edit card when the call completes.
func (c ChatModel) WriteProgress(name, path string, lines int, creating bool) ChatModel {
	steps := c.cloneSteps()
	if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
		steps[n-1].thinkDone = true
	}
	if n := len(steps); n > 0 && steps[n-1].kind == StepWriting {
		steps[n-1].name, steps[n-1].path = name, path
		steps[n-1].lines, steps[n-1].create = lines, creating
		c.live.steps = steps
		return c
	}
	steps = append(steps, liveStep{kind: StepWriting, name: name, path: path, lines: lines, create: creating})
	c.live.steps = steps
	return c
}

// EndWrite drops the live writing step, if any. The real tool step takes over
// from here.
func (c ChatModel) EndWrite() ChatModel {
	steps := c.cloneSteps()
	for len(steps) > 0 && steps[len(steps)-1].kind == StepWriting {
		steps = steps[:len(steps)-1]
	}
	c.live.steps = steps
	return c
}

// StreamAnswer appends final-answer text; it shows at once.
func (c ChatModel) StreamAnswer(s string) ChatModel {
	c.live.answer = append(c.live.answer, []rune(s)...)
	return c
}

// EndAnswer marks the answer as complete; the reply is committed to the
// history on the next Tick.
func (c ChatModel) EndAnswer() ChatModel {
	c.live.answerDone = true
	return c
}

func (s liveStep) toStep() Step {
	switch s.kind {
	case StepReadFile:
		return Step{Kind: StepReadFile, Path: s.path}
	case StepShell:
		return Step{Kind: StepShell, Command: s.cmd, Output: string(s.out)}
	case StepEdit:
		return Step{Kind: StepEdit, Path: s.path, Diff: s.diff, Create: s.create}
	}
	tl := Timeline{Collapsed: s.hidden, Took: s.took}
	if len(s.think) > 0 {
		tl.Nodes = []TimelineNode{{Content: string(s.think)}}
	}
	return Step{Kind: StepThinking, Timeline: tl}
}

// Tick advances the spinner by one frame (call every ~30ms) and commits the
// reply to the history once the stream has ended.
func (c ChatModel) Tick() ChatModel {
	c.frame++
	if !c.active || !c.live.answerDone {
		return c
	}
	l := c.live
	msg := Message{Role: RoleAssistant, Text: string(l.answer)}
	for _, s := range l.steps {
		if s.kind == StepWriting {
			continue
		}
		msg.Steps = append(msg.Steps, s.toStep())
	}
	c.messages = append(c.messages, msg)
	c.active = false
	c.live = liveReply{}
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

// writeLine renders the tool call the model is still writing:
//
//	✎ creating index.html · 128 lines
func writeLine(path string, create bool, lines int) string {
	verb, icon := "editing", "✎"
	if create {
		verb, icon = "creating", "＋"
	}
	out := " " + AccentStyle.Render(icon) + " " +
		AccentStyle.Render(verb) + " " + MutedStyle.Render(path)
	if lines > 0 {
		out += DimStyle.Render(" · ") + MutedStyle.Render(fmt.Sprintf("%d lines", lines))
	}
	return out
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
					out = append(out, plainRows(editLines(st.Path, st.Create, st.Diff, cw))...)
				}
			}
			if m.Text != "" {
				out = append(out, blank...)
				out = append(out, plainRows(answerLines(m.Text, cw))...)
			}
			if m.Stopped {
				out = append(out, blank...)
				out = append(out, plainRows([]string{stoppedLine(cw)})...)
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
				tl := Timeline{Running: !s.thinkDone, Collapsed: s.hidden, Took: s.took}
				if len(s.think) > 0 {
					tl.Nodes = []TimelineNode{{Content: string(s.think)}}
				}
				out = append(out, c.timelineRows(tl, cw, spin, -(2+j))...)
			case StepReadFile:
				out = append(out, plainRows([]string{readFileLine(s.path)})...)
			case StepShell:
				out = append(out, plainRows(shellLines(s.cmd, string(s.out), cw))...)
			case StepEdit:
				out = append(out, plainRows(editLines(s.path, s.create, s.diff, cw))...)
			case StepWriting:
				out = append(out, plainRows([]string{writeLine(s.path, s.create, s.lines)})...)
			}
		}
		if len(l.answer) > 0 {
			out = append(out, blank...)
			out = append(out, plainRows(answerLines(string(l.answer), cw))...)
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
