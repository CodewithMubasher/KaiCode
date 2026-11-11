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

func TestCreatedFileStartsCollapsedAndOpensOnDemand(t *testing.T) {
	c := NewChat(100, 40).AppendUser("go").StartReply().EditFile("b.go", true, fileLines())
	v := ansi.Strip(c.View())
	if !strings.Contains(v, "Created b.go") || strings.Contains(v, "func B()") {
		t.Fatalf("a created file should show its heading only:\n%s", v)
	}
	c.live.steps[len(c.live.steps)-1].hidden = false // what a click on the heading does
	c.live.stepVer = nextVer()
	v = ansi.Strip(c.View())
	if !strings.Contains(v, "package b") || !strings.Contains(v, "func B() {}") {
		t.Errorf("an opened node shows the whole file:\n%s", v)
	}
}

func TestEditedFileStaysOpen(t *testing.T) {
	diff := []DiffLine{{Kind: DiffDel, Num: 4, Text: "a := 1"}, {Kind: DiffAdd, Num: 4, Text: "a := 2"}}
	c := NewChat(100, 40).AppendUser("go").StartReply().EditFile("x.go", false, diff)
	v := ansi.Strip(c.View())
	if !strings.Contains(v, "Edited x.go") || !strings.Contains(v, "a := 2") {
		t.Errorf("an edit shows its diff:\n%s", v)
	}
}
