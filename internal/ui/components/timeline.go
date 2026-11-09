package components

import (
	"fmt"
	"strings"
	"time"
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
}

// TimelineNode is one step of a timeline. For now it only carries content.
type TimelineNode struct {
	Content string
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
		for _, l := range wrapText(n.Content, width-gutter) {
			out = append(out, bar+DimStyle.Render(strings.TrimRight(l, " ")))
		}
	}
	return out
}
