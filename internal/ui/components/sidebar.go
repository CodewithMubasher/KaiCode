package components

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// SidebarModel is the right-hand panel of the chat page:
//
//	KAI CODE v0.1.0          bright
//
//	Context                  bright
//	45,305 tokens            dim
//	23% used                 dim
//	  ...
//	~\desktop\KaiCode:main   dim, bright on hover
type SidebarModel struct {
	w, h      int
	sessionID string
	version   string

	tokens, total int
	cwd           string
	hoverCwd      bool
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

func (s SidebarModel) SetVersion(v string) SidebarModel {
	s.version = v
	return s
}

// SetContext sets how many tokens the provider's context holds and the size
// of its window.
func (s SidebarModel) SetContext(tokens, total int) SidebarModel {
	s.tokens, s.total = tokens, total
	return s
}

// SetCwd sets the working-directory label shown at the bottom.
func (s SidebarModel) SetCwd(label string) SidebarModel {
	s.cwd = label
	return s
}

// cwdRow is the panel row holding the working directory (-1 if no room).
func (s SidebarModel) cwdRow() int {
	r := s.h - 2
	if r < 8 {
		return -1
	}
	return r
}

// cwdText is the label cut to fit the panel.
func (s SidebarModel) cwdText() string {
	n := s.w - 2*Margin
	rs := []rune(s.cwd)
	if n < 1 || len(rs) <= n {
		return s.cwd
	}
	if n == 1 {
		return "…"
	}
	return string(rs[:n-1]) + "…"
}

// Hover tracks the mouse (x, y relative to the panel's top-left corner).
func (s SidebarModel) Hover(x, y int) SidebarModel {
	r := s.cwdRow()
	s.hoverCwd = r >= 0 && s.cwd != "" && y == r &&
		x >= Margin && x < Margin+lipgloss.Width(s.cwdText())
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

// percent formats the share of the window used: "23%" or "0.4%".
func percent(tokens, total int) string {
	if total <= 0 {
		return "0%"
	}
	p := float64(tokens) * 100 / float64(total)
	if p >= 10 || tokens == 0 {
		return fmt.Sprintf("%.0f%%", p)
	}
	return fmt.Sprintf("%.1f%%", p)
}

// commas formats 45305 as "45,305".
func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (s SidebarModel) View() string {
	bg := lipgloss.NewStyle().Background(ColSide)
	bright := bg.Foreground(ColText).Bold(true)
	dim := bg.Foreground(ColDim)
	lead := bg.Render(spaces(Margin))

	rows := make([]string, s.h)
	blank := s.fill("")
	for i := range rows {
		rows[i] = blank
	}
	set := func(i int, content string) {
		if i >= 0 && i < s.h {
			rows[i] = s.fill(lead + content)
		}
	}

	// Row 0 is empty (matches the chat's top padding).
	set(1, bright.Render("KAI CODE")+bg.Render(" ")+bg.Foreground(ColMuted).Render("v"+s.version))
	set(3, bright.Render("Context"))
	set(4, dim.Render(commas(s.tokens)+" tokens"))
	set(5, dim.Render(percent(s.tokens, s.total)+" used"))

	if r := s.cwdRow(); r >= 0 && s.cwd != "" {
		st := dim
		if s.hoverCwd {
			st = bg.Foreground(ColText)
		}
		set(r, st.Render(s.cwdText()))
	}
	return strings.Join(rows, "\n")
}
