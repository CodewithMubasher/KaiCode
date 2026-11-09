package components

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Overlay draws popup on top of base so that its last row sits just above
// row `bottom`, starting at column x. Rows of base outside that span are
// untouched. The popup is clamped if it would run off the top.
func Overlay(base []string, popup []string, x, bottom int) []string {
	if len(popup) == 0 {
		return base
	}
	if bottom > len(base) {
		bottom = len(base)
	}
	top := bottom - len(popup)
	if top < 0 {
		popup = popup[-top:]
		top = 0
	}
	pw := lipgloss.Width(popup[0])

	out := append([]string(nil), base...)
	for n, p := range popup {
		line := out[top+n]
		left := ansi.Truncate(line, x, "")
		left += spaces(x - lipgloss.Width(left))
		right := ansi.TruncateLeft(line, x+pw, "")
		out[top+n] = left + "\x1b[m" + p + "\x1b[m" + right
	}
	return out
}

// OverlayString is Overlay for a newline-joined block.
func OverlayString(base string, popup []string, x, bottom int) string {
	return strings.Join(Overlay(strings.Split(base, "\n"), popup, x, bottom), "\n")
}
