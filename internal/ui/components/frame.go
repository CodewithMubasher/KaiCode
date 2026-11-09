package components

import "strings"

// Footer draws the bottom border: ╰───┴───╯
func Footer(mainW, sideW int) string {
return AccentStyle.Render("╰" + strings.Repeat("─", mainW) + "┴" + strings.Repeat("─", sideW) + "╯")
}

// VBar draws a vertical border column h rows tall.
func VBar(h int) string {
if h < 1 {
h = 1
}
rows := make([]string, h)
for i := range rows {
rows[i] = AccentStyle.Render("│")
}
return strings.Join(rows, "\n")
}
