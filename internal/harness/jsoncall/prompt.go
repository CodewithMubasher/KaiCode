package jsoncall

import (
	"strings"

	"kai/internal/tools"
)

const toolsIntro = `## Tools

You can call tools. To call one, first write one short sentence that says why (it is shown to the user), then a single block in exactly this format:

<tool_call>
{"name":"read_file","arguments":{"path":"src/main.go"}}
</tool_call>

Rules:
- The block holds exactly one JSON object with two keys: "name" (the tool) and "arguments" (an object of parameter names and values).
- Write valid JSON: strings in double quotes, numbers and booleans as JSON values, arrays and objects nested normally.
- Copy the tags exactly: "<tool_call>" on its own line, then the JSON, then "</tool_call>". Never write a partial tag or a bare JSON object as ordinary text.
- Write one short reason sentence before every block, for example: I'll read the config to find the port.
- You may call several tools: put each call in its own <tool_call> block, each with its own reason sentence, one after another.
- Write nothing after the last </tool_call>. Stop and wait: the results arrive in the next message, each inside <tool_result> tags, in the same order as your calls.
- The tools shell, read_file, create_file and edit_file are ALWAYS available. Never tell the user that a tool is missing or unavailable, and never invent extra tools.
- Only use the exact tool names and parameter names defined below.
- Every required parameter must be present in every call.
- A result that starts with "Error:" means the call FAILED. Read the message (it shows the correct usage), fix the call, and never send the same call again unchanged.
- When the task is finished, answer normally in plain text with no tool_call block.

### Available tools
`

// ToolsPrompt is the instruction block that teaches the strict JSON calling
// format and lists every tool with its schema and an example.
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

// exampleCall shows one valid call of the tool, using the parameters' example
// values, preceded by the reason sentence the model must also write.
func exampleCall(s tools.Spec) string {
	return "I'll call " + s.Name + ".\n" +
		"<tool_call>\n" +
		`{"name":"` + s.Name + `","arguments":` + s.ExampleArgs() + "}\n" +
		"</tool_call>"
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
// call, in call order, each named after its tool, followed by a reminder of
// the protocol and the available tools. A web chat sees this as an ordinary
// user message, so the note says what it is and that the task goes on.
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
		"Continue the task: if you need another tool, write one short reason sentence and then one <tool_call> block in the JSON format; " +
		"if the task is finished, answer the user in plain text with no tool_call. " +
		"Do not repeat a call that already succeeded."
	if failed {
		note = `A call FAILED (status="error"). Read its error, fix the call, and do not send it again unchanged. ` + note
	}
	if len(specs) > 0 {
		note += "\n\nThese tools are always available; never say one is missing:\n" + tools.Manifest(specs)
	}
	return strings.Join(parts, "\n") + "\n\n" + note
}

// RepairPrompt asks the model to resend a tool call that could not be read.
func RepairPrompt(reason string) string {
	return "Your last reply could not be executed: " + reason + ".\n" +
		"Send the tool call again: one short reason sentence, then exactly this format, with nothing after the closing tag:\n" +
		"<tool_call>\n" +
		`{"name":"TOOL_NAME","arguments":{"PARAMETER_NAME":"VALUE"}}` + "\n" +
		"</tool_call>\n" +
		"all four tools (shell, read_file, create_file, edit_file) are always available. Never write raw or partial markup."
}
