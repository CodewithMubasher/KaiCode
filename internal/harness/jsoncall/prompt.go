package jsoncall

import (
	"strings"

	"kai/internal/tools"
)

// toolsIntro teaches a web chat the strict JSON calling format. The chat has
// no tool API, so everything the model needs to know is in this text.
const toolsIntro = `## Tools

You can call tools. To call tools, first write one short sentence that says why (the reason is shown to the user), then put every call in a block in exactly this format:

<tool_call>
{"name":"read_file","arguments":{"path":"src/main.go"}}
</tool_call>

Rules:
- The block holds JSON only: an object with "name" (the tool name) and "arguments" (an object). No Markdown fence, no comments, no text inside the block.
- PARALLEL TOOL CALLING IS THE PRIORITY. When you need more than one tool and the calls do not depend on each other, send ALL of them in the same reply, one <tool_call> block each (or several JSON objects in one block). They run at the same time and all results come back together. Read several files at once, run independent commands at once, create several files at once. Only send a call alone when it needs the result of an earlier call.
- Calls that touch the same file run in the order you wrote them, so you may safely create a file and edit it in the same reply.
- Write nothing after the last </tool_call>. Stop and wait: the results arrive in the next message, each inside <tool_result> tags, in the same order as your calls.
- Copy the tags exactly: <tool_call> to open and </tool_call> to close, in ASCII, never <tool_calls> and never with "|" bars.
- Strings in JSON must be escaped: use \n for a new line and \" for a quote. File contents go in the "content" or "new_string" string.
- Only use the tools and parameters defined below.
- shell, read_file, create_file and edit_file are ALWAYS available. Never tell the user that a tool is missing or unavailable, and never invent extra tools.
- When the task is finished, reply normally without a <tool_call> block.
- A result that starts with "Error:" means the call FAILED. Read the message (it shows the correct usage), fix the call, and never send the same call again unchanged.
- Every required parameter must be present in every call, spelled exactly as defined below.

### Available tools
`

// ToolsPrompt is the instruction block that teaches the model the JSON
// calling format and lists the available tools.
func ToolsPrompt(specs []tools.Spec) string {
	var b strings.Builder
	b.WriteString(toolsIntro)
	for _, s := range specs {
		b.WriteString(s.JSON())
		b.WriteString("\n")
	}
	b.WriteString("\n### Example calls (one per tool, with the required parameters)\n\n")
	for _, s := range specs {
		b.WriteString(exampleCall(s))
		b.WriteString("\n\n")
	}
	return b.String()
}

// exampleCall shows one valid call of the tool, built from the parameters'
// example values, so the model sees the exact parameter names it must use.
func exampleCall(s tools.Spec) string {
	return "<tool_call>\n{\"name\":\"" + s.Name + "\",\"arguments\":" + s.ExampleArgs() + "}\n</tool_call>"
}

// FirstPrompt builds the first message of a web-chat session. The web chat has
// no system role, so Kai's instructions and the tool block travel in front of
// the user's request.
func FirstPrompt(system string, specs []tools.Spec, user string) string {
	var b strings.Builder
	b.WriteString("<kai_instructions>\n")
	if s := strings.TrimSpace(system); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString(ToolsPrompt(specs))
	b.WriteString("</kai_instructions>\n\n")
	b.WriteString(user)
	return b.String()
}

// EncodeResults is the next message after tool calls: one <tool_result> per
// call, in call order, each named after its tool, followed by a short note on
// what to do next. A web chat sees this as an ordinary user message, so the
// note says what it is and that the task goes on: without it a model may
// answer the text instead of carrying on, or repeat a call that already worked.
func EncodeResults(rs []tools.Result, specs []tools.Spec) string {
	parts := make([]string, 0, len(rs))
	failed := false
	for _, r := range rs {
		out := r.Output
		attr := ""
		if r.Name != "" {
			attr = ` name="` + r.Name + `"`
		}
		if r.IsError {
			out = "Error: " + out
			attr += ` status="error"`
			failed = true
		}
		parts = append(parts, "<tool_result"+attr+">\n"+out+"\n</tool_result>")
	}
	note := "The results above come from your tool calls, run on the user's machine. " +
		"Continue the task: if you need more, send tool calls in the <tool_call> JSON format, " +
		"and send every independent call together in one reply; " +
		"if the task is finished, answer the user in plain text with no tool call. " +
		"Do not repeat a call that already succeeded."
	if failed {
		note = `A call FAILED (status="error"). Read its error, fix the call, and do not send it again unchanged. ` + note
	}
	msg := strings.Join(parts, "\n") + "\n\n" + note
	if len(specs) > 0 {
		msg += "\n\nThese tools are always available; never say one is missing:\n" + tools.Manifest(specs)
	}
	return msg
}

// RepairPrompt asks the model to resend a tool call that could not be read.
func RepairPrompt(reason string) string {
	return "Your last reply could not be executed: " + reason + ".\n" +
		"Send the tool call again using exactly this format, with nothing after the closing tag:\n" +
		"<tool_call>\n{\"name\":\"TOOL_NAME\",\"arguments\":{\"PARAM\":\"VALUE\"}}\n</tool_call>\n" +
		"The block must hold valid JSON only. Remember that all four tools (shell, read_file, create_file, edit_file) are always available."
}
