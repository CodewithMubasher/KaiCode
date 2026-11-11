package components

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// spinnerFrames is the star animation shown while a timeline is running.
var spinnerFrames = []string{"✦", "✧", "✶", "✷", "✸", "✹", "✷", "✶", "✧"}

const doneIcon = "✦"

// Timeline is a block made of a heading and nodes:
//
//	✦ thinking…                 (star spins while running)
//	│
//	│  dimmed node content, streamed as it is typed
//
// When finished the star stops, the heading becomes "thought for Ns", and
// the timeline can be Collapsed so that only the heading remains:
//
//	✦ thought for 5s
type Timeline struct {
	Running   bool
	Collapsed bool
	Took      time.Duration
	Nodes     []TimelineNode
	// Title, when set, replaces the "thinking…" / "thought for Ns" heading.
	// File and edit steps use it ("Created main.go").
	Title string
}

// NodeKind says how a node's content is drawn.
type NodeKind int

const (
	NodeText NodeKind = iota // dimmed wrapped text (a thought)
	NodeCode                 // numbered code lines: a whole file, or a diff
)

// TimelineNode is one step of a timeline. A text node carries Content; a code
// node carries Lines (the full content of a created file, or the diff of an
// edit) and is drawn with line numbers and red/green rows.
type TimelineNode struct {
	Content string
	Kind    NodeKind
	Lines   []DiffLine
}

// NewRunningTimeline starts an expanded timeline in the running state.
func NewRunningTimeline() Timeline {
	return Timeline{Running: true}
}

// NewDoneTimeline builds a finished, collapsed timeline.
func NewDoneTimeline(took time.Duration, nodes ...TimelineNode) Timeline {
	return Timeline{Took: took, Collapsed: true, Nodes: nodes}
}

// Finish stops the spinner and records how long it took.
func (t Timeline) Finish(took time.Duration) Timeline {
	t.Running = false
	t.Took = took
	return t
}

// Collapse hides the nodes, keeping only the heading.
func (t Timeline) Collapse() Timeline {
	t.Collapsed = true
	return t
}

// AddNode appends a node.
func (t Timeline) AddNode(content string) Timeline {
	nodes := make([]TimelineNode, len(t.Nodes), len(t.Nodes)+1)
	copy(nodes, t.Nodes)
	t.Nodes = append(nodes, TimelineNode{Content: content})
	return t
}

// Heading returns the current heading text.
func (t Timeline) Heading() string {
	if t.Title != "" {
		return t.Title
	}
	if t.Running {
		return "thinking…"
	}
	secs := int(t.Took.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf("thought for %ds", secs)
}

// Icon returns the star for the given animation frame.
func (t Timeline) Icon(frame int) string {
	if !t.Running {
		return doneIcon
	}
	return spinnerFrames[frame%len(spinnerFrames)]
}

// Expandable reports whether the timeline has nodes to show or hide.
func (t Timeline) Expandable() bool { return len(t.Nodes) > 0 }

// Render returns the timeline as styled lines, wrapped to width. hover
// highlights the heading (used when the mouse is over it).
func (t Timeline) Render(width, frame int, hover bool) []string {
	const gutter = 4 // " │  "

	head := AccentStyle.Render(t.Heading())
	if hover && t.Expandable() {
		head = HoverStyle.Render(t.Heading())
	}
	line := " " + AccentStyle.Render(t.Icon(frame)) + " " + head
	out := []string{line}
	if t.Collapsed || !t.Expandable() {
		return out
	}

	bar := " " + LineStyle.Render("│") + "  "
	blank := " " + LineStyle.Render("│")

	out = append(out, blank) // slight gap between heading and content
	for i, n := range t.Nodes {
		if i > 0 {
			out = append(out, blank)
		}
		if n.Kind == NodeCode {
			for _, l := range codeNodeLines(n.Lines, width-gutter) {
				out = append(out, bar+l)
			}
			continue
		}
		for _, l := range wrapText(n.Content, width-gutter) {
			out = append(out, bar+DimStyle.Render(strings.TrimRight(l, " ")))
		}
	}
	return out
}

// codeNodeLines draws the lines of a code node, every row exactly w cells
// wide: a line-number column, a +/- sign and the code. Added rows are tinted
// green and removed rows red; plain lines (a created file, context) have no
// tint. A line with no number and no kind is a dim notice ("… 12 more lines").
func codeNodeLines(lines []DiffLine, w int) []string {
	numW := 3
	for _, d := range lines {
		if n := len(strconv.Itoa(d.Num)); d.Num > 0 && n > numW {
			numW = n
		}
	}
	// row = number + space + sign + space + code
	codeW := w - numW - 3
	if codeW < 4 {
		codeW = 4
	}
	var out []string
	for _, d := range lines {
		text := strings.ReplaceAll(cleanText(d.Text), "\t", "    ")
		if d.Num == 0 && d.Kind == DiffContext {
			out = append(out, DimStyle.Italic(true).Render(hardWrap(text, w)[0]))
			continue
		}
		st := lipgloss.NewStyle()
		sign, signCol := " ", ColMuted
		switch d.Kind {
		case DiffAdd:
			st, sign, signCol = st.Background(ColAddBg), "+", ColAddSign
		case DiffDel:
			st, sign, signCol = st.Background(ColDelBg), "-", ColDelSign
		}
		for i, chunk := range hardWrap(text, codeW) {
			num, sg := spaces(numW), " "
			if i == 0 && d.Num > 0 {
				n := strconv.Itoa(d.Num)
				num, sg = spaces(numW-len(n))+n, sign
			}
			out = append(out, st.Foreground(ColMuted).Render(num)+
				st.Render(" ")+
				st.Foreground(signCol).Bold(true).Render(sg)+
				st.Render(" ")+
				highlightCode(chunk, st)+
				st.Render(spaces(codeW-lipgloss.Width(chunk))))
		}
	}
	return out
}

// FileTimeline is the timeline of a file step. A created file shows its full
// content; an edit shows its diff. A failed step shows a one-line reason, not
// the tool's result.
func FileTimeline(path string, create bool, lines []DiffLine, fail string, collapsed bool) Timeline {
	if path == "" {
		path = "file"
	}
	tl := Timeline{Collapsed: collapsed}
	switch {
	case fail != "" && create:
		tl.Title = "Could not create " + path
	case fail != "":
		tl.Title = "Could not edit " + path
	case create:
		tl.Title = "Created " + path
	default:
		tl.Title = "Edited " + path
	}
	switch {
	case fail != "":
		tl.Nodes = []TimelineNode{{Content: fail}}
	case len(lines) > 0:
		tl.Nodes = []TimelineNode{{Kind: NodeCode, Lines: lines}}
	}
	return tl
}
