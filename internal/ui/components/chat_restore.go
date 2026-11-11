package components

// This file is what keeps the chat stable across a restart or a session
// switch. Everything the person saw, including every timeline node and whether
// it was open, is part of the chat model; Snapshot and Restore make that state
// travel through storage unchanged.

// Snapshot returns the committed messages as they should be saved. A reply
// that is still streaming is finished first, so closing the app in the middle
// of an answer does not lose what was already on screen.
func (c ChatModel) Snapshot() []Message {
	if c.active {
		c = c.finish(false, true)
	}
	out := make([]Message, len(c.messages))
	copy(out, c.messages)
	return out
}

// Restore rebuilds the chat from saved messages. Every timeline starts
// collapsed, which is the state the person sees after a restart: a heading
// they can click to open, never a wall of old file contents.
func (c ChatModel) Restore(msgs []Message) ChatModel {
	c = c.Clear()
	for _, m := range msgs {
		m.Text = cleanText(m.Text)
		m.Steps = cloneSteps(m.Steps)
		for i := range m.Steps {
			normalizeStep(&m.Steps[i])
		}
		c.messages = append(c.messages, m)
	}
	c.offset = 0
	return c
}

func cloneSteps(in []Step) []Step {
	if len(in) == 0 {
		return nil
	}
	out := make([]Step, len(in))
	copy(out, in)
	for i := range out {
		out[i].Diff = append([]DiffLine(nil), in[i].Diff...)
		out[i].Timeline.Nodes = append([]TimelineNode(nil), in[i].Timeline.Nodes...)
		for j := range out[i].Timeline.Nodes {
			out[i].Timeline.Nodes[j].Lines = append([]DiffLine(nil), in[i].Timeline.Nodes[j].Lines...)
		}
	}
	return out
}

// normalizeStep repairs a step saved by an older version and enforces the rule
// that nodes start collapsed. A file step whose heading was not saved is
// rebuilt from its path, diff and result.
func normalizeStep(s *Step) {
	switch s.Kind {
	case StepEdit:
		if s.Timeline.Title == "" {
			s.Timeline = FileTimeline(s.Path, s.Create, s.Diff, s.Output, true)
		}
		s.Timeline.Collapsed = true
		s.Timeline.Running = false
	case StepThinking:
		s.Timeline.Running = false
		s.Timeline.Collapsed = true
	}
}

// AppendAssistant adds a finished assistant reply to the history without
// animation. It is used when a saved session is reopened.
func (c ChatModel) AppendAssistant(text string) ChatModel {
	c.messages = append(c.messages, Message{Role: RoleAssistant, Text: cleanText(text)})
	c.offset = 0
	return c
}

// AppendAssistantWithSteps restores a message with its tool calls and thinking.
func (c ChatModel) AppendAssistantWithSteps(text string, steps []Step) ChatModel {
	c.messages = append(c.messages, Message{Role: RoleAssistant, Text: cleanText(text), Steps: steps})
	c.offset = 0
	return c
}

// AppendTool adds a finished tool result (shown as a command card) to the
// history. Long output is cut, as a card must not flood the chat.
func (c ChatModel) AppendTool(label, output string) ChatModel {
	if r := []rune(output); len(r) > 2000 {
		output = string(r[:2000]) + "\n…"
	}
	c.messages = append(c.messages, Message{
		Role:  RoleAssistant,
		Steps: []Step{{Kind: StepShell, Command: label, Output: output}},
	})
	c.offset = 0
	return c
}
