package components

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Height is the number of rows View returns.
func (p PaletteModel) Height() int {
	n := len(p.matches)
	if n > p.rows {
		n = p.rows
	}
	return n
}

// View renders the popup, w cells wide, one string per row.
func (p PaletteModel) View(w int) []string {
	if !p.Visible() || w < 12 {
		return nil
	}
	nameW := 0
	for _, c := range p.matches {
		if n := lipgloss.Width(c.Name) + 1; n > nameW { // +1 for the slash
			nameW = n
		}
	}
	nameW += 2 // gap between name and description

	end := p.offset + p.Height()
	rows := make([]string, 0, end-p.offset)
	for i := p.offset; i < end; i++ {
		c := p.matches[i]

		descW := w - 1 - nameW - 1 // 1 left pad, 1 right pad
		desc := ansi.Truncate(c.Desc, descW, "…")
		name := padRight("/"+c.Name, nameW)

		var row string
		if i == p.sel {
			bg := lipgloss.NewStyle().Background(ColTeal).Foreground(ColOnTeal)
			row = bg.Render(" ") + bg.Bold(true).Render(name) + bg.Render(padRight(desc, descW+1))
		} else {
			bg := lipgloss.NewStyle().Background(ColPopup)
			row = bg.Render(" ") +
				bg.Foreground(ColText).Bold(true).Render(name) +
				bg.Foreground(ColMuted).Render(padRight(desc, descW+1))
		}
		rows = append(rows, row)
	}
	return rows
}
