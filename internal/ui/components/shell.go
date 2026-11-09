package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// shellLines renders a shell step as a dark card with a dim bar, w cells wide:
//
//	▍
//	▍  $ git push origin main        command: bold, bright
//	▍
//	▍  To https://github.com/...     output: dimmed
//	▍
//
// The output block is left out while output is empty (command still running).
func shellLines(cmd, output string, w int) []string {
	const lead, tail = 2, 2
	pw := w - 1 // panel width, after the bar
	inner := pw - lead - tail
	if inner < 1 {
		inner = 1
	}

	bg := lipgloss.NewStyle().Background(ColPanel)
	cmdStyle := bg.Foreground(ColText).Bold(true)
	outStyle := bg.Foreground(ColDim)

	row := func(text string, st lipgloss.Style) string {
		return bg.Render(spaces(lead)) + st.Render(padRight(text, inner)) + bg.Render(spaces(tail))
	}
	blank := bg.Render(spaces(pw))

	out := []string{blank}
	for _, l := range wrapText("$ "+cmd, inner) {
		out = append(out, row(strings.TrimRight(l, " "), cmdStyle))
	}
	if output != "" {
		out = append(out, blank)
		output = strings.ReplaceAll(strings.TrimRight(output, "\n"), "\t", "    ")
		for _, line := range strings.Split(output, "\n") {
			for _, l := range hardWrap(line, inner) {
				out = append(out, row(l, outStyle))
			}
		}
	}
	return withBar(append(out, blank))
}
