package ui

import "kai/internal/ui/components"

// modalOpen reports whether the command menu, a picker or a form is showing.
// The page behind a modal is inert.
func (m Model) modalOpen() bool {
	return m.dialog.Visible() || m.picker.Visible() || m.form.Visible()
}

// overlayModal draws whichever modal is open on top of content, with
// everything behind it dimmed.
func (m Model) overlayModal(content string) string {
	if m.width < minW || m.height < minH {
		return content
	}
	var rows []string
	var bw, bh int
	switch {
	case m.form.Visible():
		rows = m.form.View()
		bw, bh = m.form.Size()
	case m.picker.Visible():
		rows = m.picker.View()
		bw, bh = m.picker.Size()
	case m.dialog.Visible():
		rows = m.dialog.View()
		bw, bh = m.dialog.Size()
	default:
		return content
	}
	top := (m.height - bh) / 2
	return components.OverlayModal(content, dialogDim, rows, (m.width-bw)/2, top+bh)
}
