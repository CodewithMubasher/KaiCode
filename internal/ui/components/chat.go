package components

import (
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

type StepKind int

const (
StepThinking StepKind = iota
StepReadFile
StepShell
StepEdit
StepWriting
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

type liveStep struct {
kind StepKind

think     []rune
thinkDone bool
took      time.Duration
hidden    bool

path string
cmd  string
out  []rune
diff []DiffLine

name   string
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
offset   int

frame  int
active bool
live   liveReply

hovering bool
hover    int
}

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

func (c ChatModel) Active() bool { return c.active }

func (c ChatModel) cloneSteps() []liveStep {
steps := make([]liveStep, len(c.live.steps))
copy(steps, c.live.steps)
return steps
}

func (c ChatModel) Stop() ChatModel { return c.cut(true) }

func (c ChatModel) Fail(text string) ChatModel { return c.cut(false).AppendError(text) }

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
continue
}
if s.kind == StepThinking {
s.hidden = false
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
c.live = liveReply{}
c.frame = 0
c.offset = 0
return c
}

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

func (c ChatModel) ReadFile(path string) ChatModel {
steps := c.cloneSteps()
if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
steps[n-1].thinkDone = true
}
steps = append(steps, liveStep{kind: StepReadFile, path: path})
c.live.steps = steps
return c
}

func (c ChatModel) RunShell(cmd, output string) ChatModel {
steps := c.cloneSteps()
if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
steps[n-1].thinkDone = true
}
steps = append(steps, liveStep{kind: StepShell, cmd: cmd, out: []rune(output)})
c.live.steps = steps
return c
}

func (c ChatModel) EditFile(path string, create bool, diff []DiffLine) ChatModel {
steps := c.cloneSteps()
if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
steps[n-1].thinkDone = true
}
steps = append(steps, liveStep{kind: StepEdit, path: path, diff: diff, create: create})
c.live.steps = steps
return c
}

func (c ChatModel) WriteProgress(name, path string, creating bool) ChatModel {
steps := c.cloneSteps()
if n := len(steps); n > 0 && steps[n-1].kind == StepThinking && !steps[n-1].thinkDone {
steps[n-1].thinkDone = true
}
if n := len(steps); n > 0 && steps[n-1].kind == StepWriting {
steps[n-1].name, steps[n-1].path = name, path
c.live.steps = steps
return c
}
steps = append(steps, liveStep{kind: StepWriting, name: name, path: path, create: creating})
c.live.steps = steps
return c
}

func (c ChatModel) EndWrite() ChatModel {
steps := c.cloneSteps()
for len(steps) > 0 && steps[len(steps)-1].kind == StepWriting {
steps = steps[:len(steps)-1]
}
c.live.steps = steps
return c
}

func (c ChatModel) StreamAnswer(s string) ChatModel {
c.live.answer = append(c.live.answer, []rune(s)...)
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

func writeLine(path string, create bool) string {
verb, icon := "editing", "✎"
if create {
verb, icon = "creating", "＋"
}
return " " + AccentStyle.Render(icon) + " " +
AccentStyle.Render(verb+" file") + " " + 
DimStyle.Render("(") + MutedStyle.Render(path) + DimStyle.Render(")")
}

func plainRows(lines []string) []row {
rows := make([]row, len(lines))
for i, l := range lines {
rows[i] = row{text: l, head: headNone}
}
return rows
}

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
spin := c.frame / 3
blank := plainRows([]string{""})
out := plainRows([]string{""})

for i, m := range c.messages {
switch m.Role {
case RoleUser:
out = append(out, plainRows(panelBlock(m.Text, cw, "", "", 0))...)
case RoleAssistant:
for j, st := range m.Steps {
// We allow StepShell to render (used for file blocks)
// but still skip StepEdit diffs.
if st.Kind == StepEdit {
continue
}
if j > 0 {
out = append(out, blank...)
}
switch st.Kind {
case StepThinking:
out = append(out, c.timelineRows(st.Timeline, cw, spin, i*stepStride+j)...)
case StepReadFile:
out = append(out, plainRows([]string{readFileLine(st.Path)})...)
case StepShell:
out = append(out, plainRows(shellLines(st.Command, st.Output, cw))...)
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
out = append(out, blank...)
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
case StepWriting:
out = append(out, plainRows([]string{writeLine(s.path, s.create)})...)
}
}
if len(l.answer) > 0 {
out = append(out, blank...)
out = append(out, plainRows(answerLines(string(l.answer), cw))...)
}
}
return out
}

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
