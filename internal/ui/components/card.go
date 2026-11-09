package components

import "charm.land/lipgloss/v2"

// cardBar is the left bar of shell and edit cards: the same "▍" the input
// uses, but white at reduced opacity instead of the accent colour.
func cardBar() string {
	return lipgloss.NewStyle().Foreground(ColBarDim).Background(ColPanel).Render("▍")
}

// withBar prefixes every row with the card bar.
func withBar(rows []string) []string {
	bar := cardBar()
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = bar + r
	}
	return out
}

// hardWrap splits s into chunks of at most w cells, keeping indentation.
func hardWrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	rs := []rune(s)
	if len(rs) == 0 {
		return []string{""}
	}
	var out []string
	for len(rs) > w {
		out = append(out, string(rs[:w]))
		rs = rs[w:]
	}
	return append(out, string(rs))
}
