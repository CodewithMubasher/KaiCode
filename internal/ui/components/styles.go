package components

import (
"image/color"

"charm.land/lipgloss/v2"
)

// Margin is the horizontal gap between a panel edge and its content.
const Margin = 2

var (
ColBG     color.Color = lipgloss.Color("#000000")
ColAccent color.Color = lipgloss.Color("#b8148c")
ColPanel  color.Color = lipgloss.Color("#1a1a1a")
ColSide   color.Color = lipgloss.Color("#1c1c1c")
ColText   color.Color = lipgloss.Color("#ffffff")
ColMuted  color.Color = lipgloss.Color("#9a9a9a")
ColDim    color.Color = lipgloss.Color("#7a7a7a")
ColLine   color.Color = lipgloss.Color("#3c3c3c")
ColShadow color.Color = lipgloss.Color("#7a8a99")
ColError  color.Color = lipgloss.Color("#ff5555")
)

var (
AccentStyle = lipgloss.NewStyle().Foreground(ColAccent)
MutedStyle  = lipgloss.NewStyle().Foreground(ColMuted)
DimStyle    = lipgloss.NewStyle().Foreground(ColDim)
LineStyle   = lipgloss.NewStyle().Foreground(ColLine)
HoverStyle  = lipgloss.NewStyle().Foreground(ColAccent).Bold(true).Underline(true)
TextStyle   = lipgloss.NewStyle().Foreground(ColText)
ErrorStyle  = lipgloss.NewStyle().Foreground(ColError)
)
