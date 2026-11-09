package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

type SidebarModel struct {
	w, h      int
	sessionID string
}

func NewSidebar() SidebarModel { return SidebarModel{} }

func (s SidebarModel) SetSize(w, h int) SidebarModel {
	s.w, s.h = w, h
	return s
}

func (s SidebarModel) SetSessionID(id string) SidebarModel {
	s.sessionID = id
	return s
}

// fill pads content with the sidebar background to the full panel width.
func (s SidebarModel) fill(content string) string {
	pad := s.w - lipgloss.Width(content)
	if pad < 0 {
		pad = 0
	}
	return content + lipgloss.NewStyle().Background(ColSide).Render(spaces(pad))
}

// headingRows renders "Session ID <id>" on one line, or on two lines when
// the panel is too narrow for it.
func (s SidebarModel) headingRows() []string {
	bg := lipgloss.NewStyle().Background(ColSide)
	label := bg.Foreground(ColText).Bold(true).Render("Session ID")
	id := bg.Foreground(ColMuted).Render(s.sessionID)
	lead := bg.Render(spaces(Margin))
	gap := bg.Render(" ")

	one := lead + label + gap + id
	if lipgloss.Width(one) <= s.w {
		return []string{s.fill(one)}
	}
	return []string{s.fill(lead + label), s.fill(lead + id)}
}

func (s SidebarModel) View() string {
	blank := s.fill("")
	rows := make([]string, s.h)
	for i := range rows {
		rows[i] = blank
	}
	// Row 0 is empty (matches the chat's top padding); heading starts at row 1.
	for i, r := range s.headingRows() {
		if 1+i < s.h {
			rows[1+i] = r
		}
	}
	return strings.Join(rows, "\n")
}
