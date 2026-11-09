package components

import (
	"strings"
	"testing"
)

func drainTicks(c ChatModel) ChatModel {
	for i := 0; i < 400 && c.Active(); i++ {
		c = c.Tick()
	}
	return c
}

// A reply that never sends reasoning must not show an empty "thought" line.
func TestReplyWithoutThinkingHasNoThoughtLine(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().StreamAnswer("hello").EndAnswer()
	c = drainTicks(c)

	view := c.View()
	if strings.Contains(view, "thought") || strings.Contains(view, "thinking") {
		t.Errorf("a reply without reasoning showed a thought line:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Errorf("the answer is missing:\n%s", view)
	}
}

// Thinking still appears when the model actually sends it.
func TestThinkingStillShowsWhenPresent(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().StreamThinking("let me look")
	if !strings.Contains(c.View(), "thinking") {
		t.Errorf("the live thinking line is missing:\n%s", c.View())
	}
}

// While the model is writing a tool call, the chat shows the file and a line
// count that grows before the call is complete.
func TestWritingStepShowsFileAndLines(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply()
	c = c.WriteProgress("edit_file", "index.html", 12, true)

	view := c.View()
	for _, want := range []string{"creating", "index.html", "12 lines"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}

	c = c.WriteProgress("edit_file", "index.html", 40, true)
	if !strings.Contains(c.View(), "40 lines") {
		t.Errorf("the line count did not grow:\n%s", c.View())
	}

	c = c.EndWrite()
	if strings.Contains(c.View(), "creating") {
		t.Errorf("the writing line survived the call:\n%s", c.View())
	}
}

// An edit (non-empty old_string arrives later) switches the label.
func TestWritingStepSaysEditingForAChange(t *testing.T) {
	c := NewChat(80, 20).AppendUser("hi").StartReply().
		WriteProgress("edit_file", "main.go", 3, false)
	if !strings.Contains(c.View(), "editing") {
		t.Errorf("view does not say editing:\n%s", c.View())
	}
}
