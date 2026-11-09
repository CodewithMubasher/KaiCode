package components

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Rows shown for one card; everything past this is summarised.
const (
	maxFileRows  = 40
	maxShellRows = 40
	shellHead    = 10
	shellTail    = 25
)

func shellLines(cmd, output string, w int) []string {
	const lead, tail = 2, 2
	pw := w - 1
	inner := pw - lead - tail
	if inner < 1 {
		inner = 1
	}

	bg := lipgloss.NewStyle().Background(ColShellBg)

	// Check if this is a file block
	isFile := strings.HasPrefix(cmd, "Created ") || strings.HasPrefix(cmd, "Wrote ")

	cmdStyle := bg.Foreground(ColText).Bold(true)
	if isFile {
		cmdStyle = bg.Foreground(ColTeal).Bold(true) // Teal heading for files
	}

	outStyle := bg.Foreground(ColDim)
	fileStyle := bg.Foreground(ColText)
	numStyle := bg.Foreground(ColMuted)

	row := func(text string, st lipgloss.Style) string {
		return bg.Render(spaces(lead)) + st.Render(padRight(text, inner)) + bg.Render(spaces(tail))
	}
	blank := bg.Render(spaces(pw))

	out := []string{blank}
	for _, l := range wrapText("$ "+sanitizeOutput(cmd), inner) {
		out = append(out, row(strings.TrimRight(l, " "), cmdStyle))
	}
	if output != "" {
		out = append(out, blank)
		output = sanitizeOutput(output)
		lines := strings.Split(output, "\n")
		total := len(lines)

		numW := 1
		if isFile {
			numW = len(strconv.Itoa(total))
		}

		// A card must never flood the chat: show the start of a file, and
		// the start and end of a command's output. The rest is counted.
		type shown struct {
			n    int // 1-based line number, 0 for the "hidden" notice
			text string
		}
		var view []shown
		hidden := func(n int) shown {
			return shown{text: "… " + strconv.Itoa(n) + " more lines hidden"}
		}
		switch {
		case isFile && total > maxFileRows:
			for i := 0; i < maxFileRows; i++ {
				view = append(view, shown{i + 1, lines[i]})
			}
			view = append(view, hidden(total-maxFileRows))
		case !isFile && total > maxShellRows:
			for i := 0; i < shellHead; i++ {
				view = append(view, shown{i + 1, lines[i]})
			}
			view = append(view, hidden(total-shellHead-shellTail))
			for i := total - shellTail; i < total; i++ {
				view = append(view, shown{i + 1, lines[i]})
			}
		default:
			for i, l := range lines {
				view = append(view, shown{i + 1, l})
			}
		}

		for _, v := range view {
			if v.n == 0 {
				out = append(out, row(v.text, outStyle.Italic(true)))
				continue
			}
			if isFile {
				num := strconv.Itoa(v.n)
				numStr := numStyle.Render(spaces(numW-len(num)) + num)
				for n, l := range hardWrap(v.text, inner-numW-2) {
					lead2 := numStr + bg.Render(" ")
					if n > 0 {
						lead2 = bg.Render(spaces(numW + 1))
					}
					out = append(out, bg.Render(spaces(lead))+lead2+fileStyle.Render(padRight(l, inner-numW-1))+bg.Render(spaces(tail)))
				}
				continue
			}
			for _, l := range hardWrap(v.text, inner) {
				out = append(out, row(l, outStyle))
			}
		}
	}
	return withBar(append(out, blank), ColShellBg)
}

// sanitizeOutput makes command output safe to draw. Colour codes, carriage
// returns (progress bars redraw one line over and over) and other control
// characters would otherwise move the cursor and wreck the layout.
func sanitizeOutput(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.ContainsRune(s, '\r') {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if k := strings.LastIndexByte(l, '\r'); k >= 0 {
				lines[i] = l[k+1:]
			}
		}
		s = strings.Join(lines, "\n")
	}
	s = strings.TrimRight(s, "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return -2 // replaced below
		case r == '\n':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, strings.ReplaceAll(s, "\t", "    "))
}
