package components

import (
"strings"

"charm.land/lipgloss/v2"
)

const logoOffset = 2

// borderRow is the logo row that sits on the frame's top border.
const borderRow = logoRows / 2

// HeaderModel draws the top of the frame: the compact logo overlapping
// the top border, plus the version tag.
type HeaderModel struct {
mainW, sideW int
version      string
}

func NewHeader(version string) HeaderModel {
return HeaderModel{version: version}
}

func (h HeaderModel) SetSize(mainW, sideW int) HeaderModel {
h.mainW, h.sideW = mainW, sideW
return h
}

func (h HeaderModel) compact() bool {
return h.mainW < LogoWidth+len(h.version)+8
}

func (h HeaderModel) Height() int {
if h.compact() {
return 1
}
return logoRows
}

func (h HeaderModel) rule(from, to int) string {
var sb strings.Builder
for c := from; c < to; c++ {
if c == h.mainW {
sb.WriteString("┬")
} else {
sb.WriteString("─")
}
}
return AccentStyle.Render(sb.String())
}

func (h HeaderModel) topRule(inlay string, at int) string {
total := h.mainW + 1 + h.sideW
w := lipgloss.Width(inlay)
return AccentStyle.Render("╭") + h.rule(0, at) + inlay + h.rule(at+w, total) + AccentStyle.Render("╮")
}

func (h HeaderModel) View() string {
ver := MutedStyle.Render("v" + h.version)
if h.compact() {
title := " " + TextStyle.Bold(true).Render("KAI CODE") + " " + ver + " "
return h.topRule(title, 1)
}

vb := AccentStyle.Render("│")
side := lipgloss.NewStyle().Background(ColSide).Render(spaces(h.sideW))

rows := make([]string, 0, logoRows)
for r := 0; r < borderRow; r++ {
rows = append(rows, spaces(logoOffset)+logoStyled[r])
}
rows = append(rows, h.topRule(logoStyled[borderRow], logoOffset-1))
for r := borderRow + 1; r < logoRows; r++ {
left := spaces(logoOffset-1) + logoStyled[r]
if r == logoRows-1 {
left += " " + ver
}
rows = append(rows, vb+padRight(left, h.mainW)+vb+side+vb)
}
return strings.Join(rows, "\n")
}
