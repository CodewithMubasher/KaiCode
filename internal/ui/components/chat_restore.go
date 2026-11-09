package components

// AppendAssistant adds a finished assistant reply to the history without
// animation. It is used when a saved session is reopened.
func (c ChatModel) AppendAssistant(text string) ChatModel {
c.messages = append(c.messages, Message{Role: RoleAssistant, Text: text})
c.offset = 0
return c
}

// AppendAssistantWithSteps restores a message with its tool calls and thinking.
func (c ChatModel) AppendAssistantWithSteps(text string, steps []Step) ChatModel {
c.messages = append(c.messages, Message{Role: RoleAssistant, Text: text, Steps: steps})
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
