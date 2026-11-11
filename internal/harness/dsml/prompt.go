package dsml

import (
	"strings"

	"kai/internal/tools"
)

const toolsIntro = `## Tools

You can call tools. To call one or more tools, end your reply with a "<|DSML|tool_calls>" block exactly like this:

<|DSML|tool_calls>
<|DSML|invoke name="$TOOL_NAME">
<|DSML|parameter name="$PARAMETER_NAME" string="true">$PARAMETER_VALUE</|DSML|parameter>
<|DSML|parameter name="$PARAMETER_NAME" string="false">$JSON_VALUE</|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>

Rules:
- Use string="true" for plain text values (write them as-is, no escaping). Use string="false" for numbers, booleans, arrays and objects (write valid JSON).
- You may put several invoke entries in one block; they run in order.
- Write nothing after </|DSML|tool_calls>. Stop and wait: the results arrive in the next message, each inside <tool_result> tags, in the same order as your calls.
- Copy the tags exactly as shown: ASCII "|" bars, one on each side of DSML, and the wrapper name tool_calls.
- Only use the tools and parameters defined below.
- When the task is finished, reply normally without a tool_calls block.
- A result that starts with "Error:" means the call FAILED. Read the message (it shows the correct usage), fix the call, and never send the same call again unchanged.
- Every required parameter must be present in every call, spelled exactly as defined below.

### Available tools
`

// ToolsPrompt is the instruction block that teaches the model the DSML
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

// exampleCall shows one valid call of the tool in DSML, using the parameters'
// example values, so the model sees the exact parameter names it must use.
func exampleCall(s tools.Spec) string {
	var b strings.Builder
	b.WriteString("<|DSML|tool_calls>\n<|DSML|invoke name=\"" + s.Name + "\">\n")
	for _, p := range s.Params {
		if !p.Required {
			continue
		}
		ex := p.Example
		if ex == "" {
			ex = "<" + p.Name + ">"
		}
		str := "true"
		if p.Type == "integer" || p.Type == "number" || p.Type == "boolean" {
			str = "false"
		}
		b.WriteString("<|DSML|parameter name=\"" + p.Name + "\" string=\"" + str + "\">" + ex + "</|DSML|parameter>\n")
	}
	b.WriteString("</|DSML|invoke>\n</|DSML|tool_calls>")
	return b.String()
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
func EncodeResults(rs []tools.Result) string {
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
		"Continue the task: if you need more, send another tool call in the DSML format; " +
		"if the task is finished, answer the user in plain text with no tool call. " +
		"Do not repeat a call that already succeeded."
	if failed {
		note = `A call FAILED (status="error"). Read its error, fix the call, and do not send it again unchanged. ` + note
	}
	return strings.Join(parts, "\n") + "\n\n" + note
}

// RepairPrompt asks the model to resend a tool call that could not be read.
func RepairPrompt(reason string) string {
	return "Your last reply could not be executed: " + reason + ".\n" +
		"Send the tool call again using exactly this format, with nothing after the closing tag:\n" +
		"<|DSML|tool_calls>\n<|DSML|invoke name=\"TOOL\">\n" +
		"<|DSML|parameter name=\"PARAM\" string=\"true\">VALUE</|DSML|parameter>\n" +
		"</|DSML|invoke>\n</|DSML|tool_calls>"
}
