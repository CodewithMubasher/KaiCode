package components

import (
"strings"

"charm.land/lipgloss/v2"
)

func spaces(n int) string {
if n < 0 {
n = 0
}
return strings.Repeat(" ", n)
}

func padRight(s string, n int) string {
return s + spaces(n-lipgloss.Width(s))
}

func wrapText(s string, w int) []string {
if w < 1 {
w = 1
}
return strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
}

// panelBlock renders text on a dark panel with an accent bar on the left
// and one row of padding above and below. Every returned line is w cells wide.
// maxRows > 0 keeps only the last maxRows rows of text.
func panelBlock(text string, w int, prompt, cont string, maxRows int) []string {
const lead = 2
const rightPad = 2
bg := lipgloss.NewStyle().Background(ColPanel)
bar := lipgloss.NewStyle().Foreground(ColAccent).Background(ColPanel).Render("▍")
ps := lipgloss.NewStyle().Foreground(ColText).Background(ColPanel)

innerW := w - 1 - lead - lipgloss.Width(prompt) - rightPad
if innerW < 1 {
innerW = 1
}
wrapped := lipgloss.NewStyle().
Background(ColPanel).
Foreground(ColText).
Width(innerW).
Render(text)
rows := strings.Split(wrapped, "\n")
if maxRows > 0 && len(rows) > maxRows {
rows = rows[len(rows)-maxRows:]
}

blank := bar + bg.Render(spaces(w-1))
out := []string{blank}
for i, r := range rows {
p := cont
if i == 0 {
p = prompt
}
out = append(out, bar+bg.Render(spaces(lead))+ps.Render(p)+r+bg.Render(spaces(rightPad)))
}
out = append(out, blank)
return out
}
