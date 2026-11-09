// Package dsml reads and writes DeepSeek's DSML tool-call markup:
//
//	<|DSML|tool_calls>
//	<|DSML|invoke name="read_file">
//	<|DSML|parameter name="path" string="true">go.mod</|DSML|parameter>
//	</|DSML|invoke>
//	</|DSML|tool_calls>
//
// The parser is deliberately forgiving, because the model does not always
// spell the markup the way it was taught: the wrapper may be tool_calls (V4),
// function_calls (V3.2) or just calls, or missing altogether; the bars may be
// ASCII "|" or full-width "｜", one or several ("<｜｜DSML｜｜ calls>"); spaces
// may follow the bars.
package dsml

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"kai/internal/tools"
)

const (
	bar      = `[|｜]+`
	pre      = `<` + bar + `\s*DSML\s*` + bar + `\s*`
	closePre = `</` + bar + `\s*DSML\s*` + bar + `\s*`
	wrapper  = `(?:(?:tool|function)[_ ]?)?calls?`
	quote    = `["']`
)

var (
	openRe  = regexp.MustCompile(`(?i)` + pre + wrapper + `\s*>`)
	closeRe = regexp.MustCompile(`(?i)` + closePre + wrapper + `\s*>`)
	// A call block the model wrote without the outer wrapper.
	bareInvokeRe = regexp.MustCompile(`(?i)` + pre + `invoke\s+name\s*=`)
	invokeRe     = regexp.MustCompile(`(?is)` + pre + `invoke\s+name\s*=\s*` + quote + `([^"']*)` + quote + `\s*>(.*?)` + closePre + `invoke\s*>`)
	paramRe      = regexp.MustCompile(`(?is)` + pre + `parameter\s+name\s*=\s*` + quote + `([^"']*)` + quote +
		`(?:\s+string\s*=\s*` + quote + `(true|false)` + quote + `)?\s*>(.*?)` + closePre + `parameter\s*>`)

	// The same tags while they are still being written (no closing tag yet).
	invokeOpenRe  = regexp.MustCompile(`(?i)` + pre + `invoke\s+name\s*=\s*` + quote + `([^"']*)` + quote + `\s*>`)
	invokeCloseRe = regexp.MustCompile(`(?i)` + closePre + `invoke\s*>`)
	paramOpenRe   = regexp.MustCompile(`(?i)` + pre + `parameter\s+name\s*=\s*` + quote + `([^"']*)` + quote + `[^>]*>`)
	paramCloseRe  = regexp.MustCompile(`(?i)` + closePre + `parameter\s*>`)

	// <|end▁of▁sentence|> and friends: model control tokens that sometimes
	// leak into the text.
	specialRe = regexp.MustCompile(`(?i)<` + bar + `\s*(?:begin|end)[▁_ ]?of[▁_ ]?sentence\s*` + bar + `>`)
)

func clean(s string) string {
	return specialRe.ReplaceAllString(strings.ReplaceAll(s, "\ufeff", ""), "")
}

// parseInvokes extracts every complete invoke from the body of a tool_calls
// block. ids numbers the calls.
func parseInvokes(body string, nextID func() string) ([]tools.Call, []string) {
	var calls []tools.Call
	var warns []string
	for _, m := range invokeRe.FindAllStringSubmatch(body, -1) {
		args := map[string]any{}
		for _, pm := range paramRe.FindAllStringSubmatch(m[2], -1) {
			name, kind, val := pm[1], pm[2], pm[3]
			if kind == "false" {
				var v any
				if err := json.Unmarshal([]byte(strings.TrimSpace(val)), &v); err == nil {
					args[name] = v
					continue
				}
				warns = append(warns, fmt.Sprintf("parameter %q of %q is marked string=\"false\" but is not valid JSON; passed as text", name, m[1]))
			}
			args[name] = val
		}
		calls = append(calls, tools.Call{ID: nextID(), Name: m[1], Args: args})
	}
	return calls, warns
}
