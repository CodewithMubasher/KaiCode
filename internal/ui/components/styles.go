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

// Slash-command popup.
ColTeal   color.Color = lipgloss.Color("#14b8a6") // highlight
ColOnTeal color.Color = lipgloss.Color("#000000") // text on the highlight
ColPopup  color.Color = lipgloss.Color("#111111") // popup background

// Shell / edit cards: dim white bar and diff colours.
ColShellBg color.Color = lipgloss.Color("#131313") // shell card, a touch darker than ColPanel
ColBarDim  color.Color = lipgloss.Color("#5a5a5a") // white at reduced opacity
ColDelBg   color.Color = lipgloss.Color("#3b222a")
ColAddBg   color.Color = lipgloss.Color("#1c3440")
ColDelSign color.Color = lipgloss.Color("#e06c75")
ColAddSign color.Color = lipgloss.Color("#7ec699")

// Code highlighting inside edit cards.
ColCode    color.Color = lipgloss.Color("#e06c75") // identifiers, strings
ColPunct   color.Color = lipgloss.Color("#56b6c2") // punctuation
ColNumber  color.Color = lipgloss.Color("#d19a66")
ColKeyword color.Color = lipgloss.Color("#c678dd")
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
