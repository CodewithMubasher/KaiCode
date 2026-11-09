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
// call, in call order.
func EncodeResults(rs []tools.Result) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		out := r.Output
		if r.IsError {
			out = "Error: " + out
		}
		parts = append(parts, "<tool_result>\n"+out+"\n</tool_result>")
	}
	return strings.Join(parts, "\n")
}

// RepairPrompt asks the model to resend a tool call that could not be read.
func RepairPrompt(reason string) string {
	return "Your last reply could not be executed: " + reason + ".\n" +
		"Send the tool call again using exactly this format, with nothing after the closing tag:\n" +
		"<|DSML|tool_calls>\n<|DSML|invoke name=\"TOOL\">\n" +
		"<|DSML|parameter name=\"PARAM\" string=\"true\">VALUE</|DSML|parameter>\n" +
		"</|DSML|invoke>\n</|DSML|tool_calls>"
}
