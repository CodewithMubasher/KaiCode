package components

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type span struct {
	text string
	st   lipgloss.Style
}

// parseInline splits s into styled spans: **bold** and `code`. Unclosed
// markers stay literal, so half-typed text renders sensibly while streaming.
func parseInline(s string, base lipgloss.Style) []span {
	var out []span
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, span{buf.String(), base})
			buf.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "**"):
			if j := strings.Index(s[i+2:], "**"); j > 0 {
				flush()
				out = append(out, span{s[i+2 : i+2+j], base.Bold(true)})
				i += j + 4
				continue
			}
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				flush()
				out = append(out, span{s[i+1 : i+1+j], lipgloss.NewStyle().Foreground(ColTeal)})
				i += j + 2
				continue
			}
		}
		buf.WriteByte(s[i])
		i++
	}
	flush()
	return out
}

// wrapSpans word-wraps styled spans to w cells. The first line starts with
// `first`, later lines with `rest` (same visible width: a hanging indent).
func wrapSpans(spans []span, first, rest string, w int) []string {
	avail := w - lipgloss.Width(first)
	if avail < 1 {
		avail = 1
	}
	var lines []string
	cur, curW, prefix := "", 0, first
	emit := func() {
		lines = append(lines, prefix+cur)
		prefix, cur, curW = rest, "", 0
	}
	space := false
	for _, sp := range spans {
		for k, part := range strings.Split(sp.text, " ") {
			if k > 0 {
				space = true
			}
			if part == "" {
				continue
			}
			pw := lipgloss.Width(part)
			if curW > 0 && space && curW+1+pw > avail {
				emit()
			}
			if curW > 0 && space {
				cur += " "
				curW++
			}
			cur += sp.st.Render(part)
			curW += pw
			space = false
		}
	}
	if cur != "" || len(lines) == 0 {
		emit()
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], w, "")
	}
	return lines
}

var (
	mdHeading = lipgloss.NewStyle().Foreground(ColAccent).Bold(true)
	mdCode    = lipgloss.NewStyle().Foreground(ColMuted).Background(ColPanel)
)

// renderMarkdown renders a small markdown subset (headings, bullets,
// numbered lists, **bold**, `code`, fenced code) as lines at most w wide.
func renderMarkdown(text string, w int) []string {
	var out []string
	inCode, prevBlank := false, true
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \r")
		trim := strings.TrimLeft(line, " ")
		indent := len(line) - len(trim)

		if strings.HasPrefix(trim, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, mdCode.Render(padRight(ansi.Truncate("  "+line, w, ""), w)))
			prevBlank = false
			continue
		}
		if trim == "" {
			if !prevBlank {
				out = append(out, "")
			}
			prevBlank = true
			continue
		}
		prevBlank = false

		switch {
		case strings.HasPrefix(trim, "# "), strings.HasPrefix(trim, "## "), strings.HasPrefix(trim, "### "):
			h := strings.TrimLeft(trim, "# ")
			st := mdHeading
			if strings.HasPrefix(trim, "### ") {
				st = TextStyle.Bold(true)
			}
			out = append(out, wrapSpans(parseInline(h, st), "", "", w)...)
		case strings.HasPrefix(trim, "- "), strings.HasPrefix(trim, "* "):
			first := spaces(indent) + AccentStyle.Render("•") + " "
			out = append(out, wrapSpans(parseInline(trim[2:], TextStyle), first, spaces(indent+2), w)...)
		default:
			if n, rest, ok := numberedItem(trim); ok {
				first := spaces(indent) + AccentStyle.Render(n+".") + " "
				out = append(out, wrapSpans(parseInline(rest, TextStyle), first, spaces(indent+len(n)+2), w)...)
			} else {
				out = append(out, wrapSpans(parseInline(trim, TextStyle), "", "", w)...)
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// numberedItem matches "12. text".
func numberedItem(s string) (num, rest string, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(s) || s[i] != '.' || s[i+1] != ' ' {
		return "", "", false
	}
	return s[:i], s[i+2:], true
}
