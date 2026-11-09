package components

import (
"fmt"
"strings"

"charm.land/lipgloss/v2"
)

type SidebarModel struct {
w, h      int
sessionID string
version   string

tokens, total int
cwd           string
hoverCwd      bool
provider      string
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

func (s SidebarModel) GetContext() (int, int) {
return s.tokens, s.total
}

func (s SidebarModel) SetContext(tokens, total int) SidebarModel {
s.tokens, s.total = tokens, total
return s
}

func (s SidebarModel) SetCwd(label string) SidebarModel {
s.cwd = label
return s
}

func (s SidebarModel) cwdRow() int {
r := s.h - 2
if r < 8 {
return -1
}
return r
}

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

func (s SidebarModel) Hover(x, y int) SidebarModel {
r := s.cwdRow()
s.hoverCwd = r >= 0 && s.cwd != "" && y == r &&
x >= Margin && x < Margin+lipgloss.Width(s.cwdText())
return s
}

func (s SidebarModel) fill(content string) string {
pad := s.w - lipgloss.Width(content)
if pad < 0 {
pad = 0
}
return content + lipgloss.NewStyle().Background(ColSide).Render(spaces(pad))
}

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

set(1, bright.Render("KAI CODE")+bg.Render(" ")+bg.Foreground(ColMuted).Render("v"+s.version))
set(3, bright.Render("Context"))
set(4, dim.Render(commas(s.tokens)+" tokens"))
set(5, dim.Render(percent(s.tokens, s.total)+" used"))

if s.provider != "" && s.h >= 12 {
set(7, bright.Render("Provider"))
set(8, dim.Render(clipRunes(s.provider, s.w-2*Margin)))
}

if r := s.cwdRow(); r >= 0 && s.cwd != "" {
st := dim
if s.hoverCwd {
st = bg.Foreground(ColText)
}
set(r, st.Render(s.cwdText()))
}
return strings.Join(rows, "\n")
}
