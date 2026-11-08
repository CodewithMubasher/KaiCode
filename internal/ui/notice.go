package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"kai/internal/ui/components"
)

// noticeClearMsg hides the notice it was started for (seq guards against an
// older timer hiding a newer notice).
type noticeClearMsg struct{ seq int }

// setNotice shows a short teal message under the input for two seconds.
func (m Model) setNotice(s string) (Model, tea.Cmd) {
	m.noticeSeq++
	m.notice = s
	seq := m.noticeSeq
	return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return noticeClearMsg{seq} })
}

// noticeRow is the row under the input, w wide: the notice, or blank.
func (m Model) noticeRow(w int) string {
	if m.notice == "" {
		return strings.Repeat(" ", w)
	}
	t := strings.Repeat(" ", components.Margin) +
		lipgloss.NewStyle().Foreground(components.ColTeal).Render(m.notice)
	return t + strings.Repeat(" ", max(0, w-lipgloss.Width(t)))
}
