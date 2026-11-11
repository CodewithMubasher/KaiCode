package components

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// assertLayout checks the invariants that keep the TUI from "breaking": the
// view is always exactly height rows, each exactly width cells, no row holds
// a newline.
func assertLayout(t *testing.T, c ChatModel, what string) {
	t.Helper()
	v := c.View()
	rows := strings.Split(v, "\n")
	if len(rows) != c.height {
		t.Fatalf("%s: view has %d rows, want %d", what, len(rows), c.height)
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != c.width {
			t.Fatalf("%s: row %d is %d cells wide, want %d: %q", what, i, w, c.width, r)
		}
	}
}

func TestEveryToolBlockAppearsOnce(t *testing.T) {
	c := NewChat(100, 60).AppendUser("go").StartReply()
	c = c.WriteProgress("edit_file", "main.go", 3, true)
	c = c.EndWrite().RunShell("Created main.go", "package main\n")
	c = c.RunShell("go build", "ok")
	c = c.StreamAnswer("done").EndAnswer().Tick()

	v := c.View()
	for _, s := range []string{"Created main.go", "go build"} {
		if n := strings.Count(v, s); n != 1 {
			t.Errorf("%q appears %d times, want 1:\n%s", s, n, v)
		}
	}
	if strings.Contains(v, "creating file") {
		t.Errorf("the in-progress line survived:\n%s", v)
	}
}

func TestShellStartIsReplacedByItsResult(t *testing.T) {
	c := NewChat(100, 30).AppendUser("go").StartReply().ShellStart("sleep 5")
	if !strings.Contains(c.View(), "running") {
		t.Fatalf("a running command should be visible:\n%s", c.View())
	}
	c = c.RunShell("sleep 5", "")
	v := c.View()
	if strings.Contains(v, "running") {
		t.Errorf("running line survived its result:\n%s", v)
	}
	if strings.Count(v, "sleep 5") != 1 {
		t.Errorf("command shown %d times:\n%s", strings.Count(v, "sleep 5"), v)
	}
}

func TestEmptyReplyAddsNoMessage(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().EndAnswer().Tick()
	if len(c.messages) != 1 {
		t.Fatalf("messages = %d, want just the user's", len(c.messages))
	}
	if c.Active() {
		t.Error("reply should be finished")
	}
}

func TestStopAndTickNeverDoubleCommit(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().StreamAnswer("part")
	c = c.Stop().Stop().Tick().Tick()
	if len(c.messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(c.messages))
	}
}

func TestLayoutHoldsForHostileContent(t *testing.T) {
	nasty := []string{
		"plain",
		"\x1b[31mred\x1b[0m and \x1b]0;title\x07 osc",
		"progress 10%\rprogress 50%\rprogress 100%",
		"日本語の長い行日本語の長い行日本語の長い行日本語の長い行日本語の長い行日本語の長い行日本語の長い行",
		"emoji 😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀",
		strings.Repeat("x", 5000),
		"tab\tseparated\tvalues\tthat\tgo\ton\tand\ton",
		"nul\x00byte and bell\x07",
		"",
		strings.Repeat("line\n", 500),
	}
	for _, w := range []int{20, 40, 80, 131} {
		c := NewChat(w, 24)
		for i, s := range nasty {
			c = c.AppendUser(s)
			c = c.StartReply().StreamThinking(s).EndThinking(time.Second)
			c = c.ReadFile(s).RunShell(s, s).StreamAnswer("**" + s + "** `" + s + "`\n- " + s + "\n").EndAnswer().Tick()
			assertLayout(t, c, fmt.Sprintf("w=%d after %d", w, i))
		}
		c = c.Fail(strings.Join(nasty, "\n"))
		assertLayout(t, c, fmt.Sprintf("w=%d error", w))
		for off := 0; off < 400; off += 37 {
			c = c.ScrollUp(37)
			assertLayout(t, c, "scrolled")
		}
	}
}

func TestResizeKeepsLayoutValid(t *testing.T) {
	c := NewChat(80, 20)
	for i := 0; i < 30; i++ {
		c = c.AppendUser(strings.Repeat("word ", 40))
		c = c.StartReply().StreamAnswer(strings.Repeat("answer text ", 60)).EndAnswer().Tick()
	}
	for _, sz := range [][2]int{{60, 14}, {200, 50}, {30, 8}, {80, 20}, {12, 3}} {
		c = c.SetSize(sz[0], sz[1])
		assertLayout(t, c, fmt.Sprintf("%dx%d", sz[0], sz[1]))
	}
}

// While reading old messages, a streaming reply must not drag the view.
func TestScrollPositionHoldsWhileStreaming(t *testing.T) {
	c := NewChat(80, 10)
	for i := 0; i < 20; i++ {
		c = c.AppendUser(fmt.Sprintf("question %d", i))
	}
	c = c.StartReply().SetSize(80, 10)
	c = c.ScrollUp(15)
	before := c.View()
	for i := 0; i < 30; i++ {
		c = c.StreamAnswer("a new line of the answer\n").SetSize(80, 10)
	}
	if after := c.View(); after != before {
		t.Errorf("the view moved while scrolled up:\n--before--\n%s\n--after--\n%s", before, after)
	}
	c = c.ScrollToBottom()
	if !strings.Contains(ansi.Strip(c.View()), "a new line of the answer") {
		t.Error("following the bottom should show the newest text")
	}
}

func TestHugeFileBlockIsSummarised(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(&b, "line %d of a very long generated file\n", i)
	}
	c := NewChat(100, 200).AppendUser("make it").StartReply()
	start := time.Now()
	c = c.RunShell("Created big.txt", b.String())
	rows := c.liveRowCount()
	if rows > maxFileRows+20 {
		t.Errorf("a 50k line file produced %d rows", rows)
	}
	if !strings.Contains(c.View(), "more lines hidden") {
		t.Errorf("the hidden part is not announced:\n%s", c.View())
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("laying out a huge file took %v", d)
	}
	if len(c.live.steps[0].out) > maxStoredOutput+200 {
		t.Errorf("stored %d bytes of output", len(c.live.steps[0].out))
	}
}

func TestHugeCommandOutputKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "out %d\n", i)
	}
	c := NewChat(100, 80).AppendUser("run").StartReply().RunShell("make", b.String())
	v := c.View()
	for _, want := range []string{"out 0", "out 19999", "more lines hidden"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

// Incremental markdown must equal a from-scratch render at every prefix.
func TestIncrementalMarkdownMatchesWholeRender(t *testing.T) {
	doc := "# Title\n\nSome **bold** text and `code` here.\n\n- one\n- two with a long line that has to wrap around the edge of the window\n\n" +
		"1. first\n2. second\n\n```go\nfunc main() {\n\n\tprintln(\"hi\")\n}\n```\n\n\n\nafter code\n\n```\nunclosed block\nstill code\n"
	for _, w := range []int{20, 37, 80} {
		var m mdCache
		var buf textBuf
		for i := 0; i < len(doc); i++ {
			buf.add(string(doc[i]))
			got := renderedAnswer(&m, buf, w+3)
			var want []string
			for _, l := range renderMarkdown(buf.String(), w) {
				want = append(want, answerPrefix+l)
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("w=%d prefix %d (%q):\n--got--\n%s\n--want--\n%s", w, i, doc[:i+1], strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
	}
}

func TestIncrementalMarkdownRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	pieces := []string{"word ", "**b** ", "`c` ", "\n", "\n\n", "- item ", "1. n ", "```\n", "# H ", "long long long long long long ", "\t", "  "}
	for n := 0; n < 200; n++ {
		var text strings.Builder
		for k := 0; k < 40; k++ {
			text.WriteString(pieces[r.Intn(len(pieces))])
		}
		doc := text.String()
		var m mdCache
		var buf textBuf
		for i := 0; i < len(doc); i += 1 + r.Intn(5) {
			end := i + 1 + r.Intn(5)
			if end > len(doc) {
				end = len(doc)
			}
			buf = bufOf(doc[:end])
			got := strings.Join(renderedAnswer(&m, buf, 43), "\n")
			var want []string
			for _, l := range renderMarkdown(doc[:end], 40) {
				want = append(want, answerPrefix+l)
			}
			if got != strings.Join(want, "\n") {
				t.Fatalf("doc %q prefix %d differs", doc, end)
			}
		}
	}
}

// ---- long chats and long streams stay fast

func TestLongChatStaysFast(t *testing.T) {
	c := NewChat(120, 40)
	for i := 0; i < 3000; i++ {
		c = c.AppendUser(fmt.Sprintf("question number %d about the project", i))
		c = c.AppendAssistantWithSteps("Here is **the answer** with `code`.\n\n- point one\n- point two\n\nThanks.", []Step{
			{Kind: StepReadFile, Path: "internal/ui/app.go"},
			{Kind: StepShell, Command: "go test ./...", Output: "ok  \tkai/internal/ui\t0.4s"},
			{Kind: StepThinking, Timeline: NewDoneTimeline(3*time.Second, TimelineNode{Content: "thinking about it for a while"})},
		})
	}
	start := time.Now()
	c = c.SetSize(120, 40) // first layout renders everything once
	first := time.Since(start)

	start = time.Now()
	for i := 0; i < 500; i++ {
		c = c.Hover(10, i%40)
		_ = c.View()
		c = c.ScrollUp(3)
	}
	steady := time.Since(start)
	t.Logf("3000 turns: first layout %v, 500 hover+view+scroll %v", first, steady)
	if steady > 2*time.Second {
		t.Errorf("steady-state interaction is slow: %v for 500 frames", steady)
	}
}

func TestLongStreamingAnswerStaysFast(t *testing.T) {
	c := NewChat(120, 40).AppendUser("write a lot").StartReply().SetSize(120, 40)
	para := "This is a streamed sentence with **bold** and `code` that keeps going.\n"
	start := time.Now()
	n := 0
	for i := 0; i < 4000; i++ {
		for _, tok := range []string{"This ", "is ", "a ", "token", "\n"} {
			c = c.StreamAnswer(tok).SetSize(120, 40)
			_ = c.View()
			n++
		}
		if i%10 == 0 {
			c = c.StreamAnswer(para+"\n").SetSize(120, 40)
		}
	}
	d := time.Since(start)
	t.Logf("%d tokens (%d KB) streamed and drawn in %v", n, c.live.answer.len()/1024, d)
	if d > 20*time.Second {
		t.Errorf("streaming a long answer is slow: %v", d)
	}
}

func TestLongThoughtStaysBoundedWhileRunning(t *testing.T) {
	c := NewChat(100, 40).AppendUser("think").StartReply()
	start := time.Now()
	for i := 0; i < 20000; i++ {
		c = c.StreamThinking("reasoning step that goes on and on and on ")
		if i%20 == 0 {
			_ = c.View()
		}
	}
	rows := c.liveRowCount()
	if rows > liveThinkRows+6 {
		t.Errorf("a running thought produced %d rows", rows)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("streaming a long thought took %v", d)
	}
	c = c.EndThinking(time.Minute)
	if c.liveRowCount() != 0 {
		t.Errorf("a finished thought must leave nothing in the timeline, got %d rows", c.liveRowCount())
	}
}

func TestResizeOfLongChatIsCached(t *testing.T) {
	c := NewChat(100, 30)
	for i := 0; i < 1000; i++ {
		c = c.AppendUser("hello there").AppendAssistant("an answer " + strings.Repeat("word ", 30))
	}
	c = c.SetSize(100, 30)
	start := time.Now()
	for i := 0; i < 200; i++ {
		c = c.SetSize(100, 30+i%5) // height changes must not re-render messages
		_ = c.View()
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("height-only resizes re-rendered the chat: %v", d)
	}
}

func TestCollapseToggleOnOldMessage(t *testing.T) {
	c := NewChat(80, 40)
	diff := []DiffLine{{Kind: DiffDel, Num: 1, Text: "secret old line"}, {Kind: DiffAdd, Num: 1, Text: "secret new line"}}
	c = c.AppendAssistantWithSteps("a", []Step{{Kind: StepEdit, Path: "x.go", Diff: diff,
		Timeline: FileTimeline("x.go", false, diff, "", false)}})
	for i := 0; i < 5; i++ {
		c = c.AppendUser("later")
	}
	c = c.SetSize(80, 40)
	if !strings.Contains(c.View(), "secret new line") {
		t.Fatal("an expanded edit should show its diff")
	}
	// find the heading row and click it
	for y, r := range c.window() {
		if r.head != headNone {
			c = c.Click(Margin+1, y)
			break
		}
	}
	if strings.Contains(c.View(), "secret new line") {
		t.Error("clicking the heading should collapse it")
	}
	assertLayout(t, c, "after click")
}

func bufOf(s string) textBuf { return textBuf{b: []byte(s)} }

// renderedAnswer flattens the cache's two parts back into plain lines.
func renderedAnswer(m *mdCache, buf textBuf, cw int) []string {
	stable, tail := m.render(buf, 0, cw)
	var out []string
	for _, r := range stable {
		out = append(out, r.text)
	}
	for _, r := range tail {
		out = append(out, r.text)
	}
	return out
}

// liveRowCount is the number of rows the live reply takes.
func (c ChatModel) liveRowCount() int {
	return c.totalRows() - len(c.committedRows())
}
