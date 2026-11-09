package components

import (
	"strings"
	"testing"
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

func TestDemoteAnswerMovesTextIntoAThought(t *testing.T) {
	c := NewChat(80, 20).StartReply().StreamAnswer("let me look")
	before := len(c.live.steps)
	c = c.DemoteAnswer()
	if c.live.answer.len() != 0 || len(c.live.steps) != before+1 {
		t.Fatalf("answer = %q, steps = %d (was %d)", c.live.answer.String(), len(c.live.steps), before)
	}
	if got := c.live.steps[before].think.String(); got != "let me look" {
		t.Errorf("demoted text = %q", got)
	}
}
