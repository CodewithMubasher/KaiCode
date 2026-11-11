package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func fileLines() []DiffLine {
	return []DiffLine{
		{Kind: DiffContext, Num: 1, Text: "package b"},
		{Kind: DiffContext, Num: 2, Text: ""},
		{Kind: DiffContext, Num: 3, Text: "func B() {}"},
	}
}

// A created file is only a heading: its content is never drawn and the node
// cannot be opened, whatever state it is in.
func TestCreatedFileShowsHeadingOnlyAndCannotOpen(t *testing.T) {
	c := NewChat(100, 40).AppendUser("go").StartReply().EditFile("b.go", true, fileLines())
	v := ansi.Strip(c.View())
	if !strings.Contains(v, "Created b.go") || strings.Contains(v, "func B()") {
		t.Fatalf("a created file should show its heading only:\n%s", v)
	}
	c.live.steps[len(c.live.steps)-1].hidden = false // even if it were "open"
	c.live.steps[len(c.live.steps)-1].touch()
	c.live.stepVer = nextVer()
	v = ansi.Strip(c.View())
	if strings.Contains(v, "package b") || strings.Contains(v, "func B() {}") {
		t.Errorf("a created file's content must never be shown:\n%s", v)
	}
	for y, r := range c.window() {
		if r.head != headNone {
			t.Errorf("row %d of a created file is clickable", y)
		}
	}
}

func TestEditedFileStartsCollapsedAndOpensOnDemand(t *testing.T) {
	diff := []DiffLine{{Kind: DiffDel, Num: 4, Text: "a := 1"}, {Kind: DiffAdd, Num: 4, Text: "a := 2"}}
	c := NewChat(100, 40).AppendUser("go").StartReply().EditFile("x.go", false, diff)
	v := ansi.Strip(c.View())
	if !strings.Contains(v, "Edited x.go") || strings.Contains(v, "a := 2") {
		t.Fatalf("an edited file should start as a heading only:\n%s", v)
	}
	c.live.steps[len(c.live.steps)-1].hidden = false
	c.live.steps[len(c.live.steps)-1].touch()
	c.live.stepVer = nextVer()
	v = ansi.Strip(c.View())
	if !strings.Contains(v, "a := 1") || !strings.Contains(v, "a := 2") {
		t.Errorf("an opened diff shows both sides:\n%s", v)
	}
}

// A restored chat is stable: the timeline and its nodes survive a round trip,
// and every node starts collapsed until the person clicks it.
func TestSnapshotRestoreKeepsTimelineCollapsed(t *testing.T) {
	c := NewChat(100, 40).AppendUser("go").StartReply().EditFile("x.go", false, []DiffLine{
		{Kind: DiffDel, Num: 4, Text: "old line"}, {Kind: DiffAdd, Num: 4, Text: "new line"},
	})
	c = c.StreamAnswer("done").EndAnswer().Tick()
	saved := c.Snapshot()
	if len(saved) != 2 || len(saved[1].Steps) != 1 {
		t.Fatalf("snapshot lost a message or its steps: %+v", saved)
	}
	r := NewChat(100, 40).Restore(saved)
	v := ansi.Strip(r.View())
	if !strings.Contains(v, "Edited x.go") || strings.Contains(v, "new line") {
		t.Errorf("a restored node should be collapsed:\n%s", v)
	}
	if !strings.Contains(v, "done") {
		t.Errorf("the answer did not survive the round trip:\n%s", v)
	}
}
