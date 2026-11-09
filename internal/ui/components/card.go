package components

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// cardBar is the left bar of shell and edit cards: the same "▍" the input
// uses, but white at reduced opacity instead of the accent colour.
func cardBar(bg color.Color) string {
	return lipgloss.NewStyle().Foreground(ColBarDim).Background(bg).Render("▍")
}

// withBar prefixes every row with the card bar.
func withBar(rows []string, bg color.Color) []string {
	bar := cardBar(bg)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = bar + r
	}
	return out
}

// hardWrap splits s into chunks of at most w cells (not runes: wide
// characters take two), so no chunk can overflow its card.
func hardWrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	if s == "" {
		return []string{""}
	}
	var out []string
	var cur strings.Builder
	curW := 0
	for _, r := range s {
		rw := 1
		if r >= 0x1100 || r < 0x20 {
			rw = ansi.StringWidth(string(r))
		}
		if curW+rw > w && curW > 0 {
			out = append(out, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(r)
		curW += rw
	}
	return append(out, cur.String())
}
