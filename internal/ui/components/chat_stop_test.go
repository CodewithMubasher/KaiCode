package components

import (
	"strings"
	"testing"
	"time"
)

func TestStopKeepsPartialReplyAndMarksIt(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply()
	c = c.StreamThinking("abc").Stop()

	if c.Active() {
		t.Fatal("reply should no longer be active")
	}
	last := c.messages[len(c.messages)-1]
	if last.Role != RoleAssistant || !last.Stopped {
		t.Fatalf("last message = %+v", last)
	}
	if !strings.Contains(c.View(), "STOPPED") {
		t.Error("the view should show the STOPPED marker")
	}
}

func TestFailAddsErrorWithoutStoppedMarker(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().Fail("boom")
	last := c.messages[len(c.messages)-1]
	if last.Role != RoleError || last.Text != "boom" {
		t.Fatalf("last message = %+v", last)
	}
	if strings.Contains(c.View(), "STOPPED") {
		t.Error("an error must not show the STOPPED marker")
	}
}

// The sentence the model writes before a tool call stays on screen as plain
// text. It must never become a thought.
func TestDemoteAnswerKeepsTextAsNoteNotThought(t *testing.T) {
	c := NewChat(80, 20).StartReply().StreamAnswer("let me look")
	c = c.DemoteAnswer()
	if c.live.answer.len() != 0 {
		t.Fatalf("the answer buffer should be empty, got %q", c.live.answer.String())
	}
	if len(c.live.steps) != 1 || c.live.steps[0].kind != StepNote || c.live.steps[0].note != "let me look" {
		t.Fatalf("steps = %+v", c.live.steps)
	}
	v := c.View()
	if !strings.Contains(v, "let me look") {
		t.Errorf("the reason text disappeared:\n%s", v)
	}
	if strings.Contains(v, "thinking") || strings.Contains(v, "thought") {
		t.Errorf("the reason text was drawn as a thought:\n%s", v)
	}
	// It survives into the committed message, in order, before later steps.
	c = c.ReadFile("a.go").EndAnswer()
	c = drainTicks(c)
	last := c.messages[len(c.messages)-1]
	if len(last.Steps) != 2 || last.Steps[0].Kind != StepNote || last.Steps[1].Kind != StepReadFile {
		t.Fatalf("committed steps = %+v", last.Steps)
	}
}

// A thought is only a live heading: nothing of it is kept in the history.
func TestThoughtIsNotCommitted(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().StreamThinking("hmm").EndThinking(time.Second)
	c = c.StreamAnswer("done").EndAnswer()
	c = drainTicks(c)
	last := c.messages[len(c.messages)-1]
	for _, s := range last.Steps {
		if s.Kind == StepThinking {
			t.Fatalf("a thought was saved: %+v", last.Steps)
		}
	}
}

// Creating a file shows its name only: no content, nothing to expand.
func TestCreatedFileNodeIsNotExpandable(t *testing.T) {
	lines := []DiffLine{{Kind: DiffContext, Num: 1, Text: "package secretbody"}}
	c := NewChat(80, 20).AppendUser("hi").StartReply().EditFile("a.go", true, lines)
	if strings.Contains(c.View(), "secretbody") {
		t.Error("the content of a created file must not be shown")
	}
	if FileTimeline("a.go", true, lines, "", false).Expandable() {
		t.Error("a created file node must not be expandable")
	}
}
