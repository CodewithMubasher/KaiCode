package components

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const logoText = "KAI CODE"
const logoRows = 3

// glyphs is a compact 3-row half-block font. Each glyph carries a trailing
// column of padding so letters stay separated without extra logic.
var glyphs = map[rune][]string{
	'K': {
		"█▄▀ ",
		"██  ",
		"█ █ ",
	},
	'A': {
		"▄▀▄ ",
		"█▀█ ",
		"▀ ▀ ",
	},
	'I': {
		"█ ",
		"█ ",
		"█ ",
	},
	'C': {
		"█▀▀ ",
		"█   ",
		"▀▄▄ ",
	},
	'O': {
		"█▀█ ",
		"█ █ ",
		"▀▄▀ ",
	},
	'D': {
		"█▀▄ ",
		"█ █ ",
		"█▄▀ ",
	},
	'E': {
		"█▀▀ ",
		"██  ",
		"█▄▄ ",
	},
}

var (
	logoPlain  [logoRows]string
	logoStyled [logoRows]string
	LogoWidth  int
)

func glyphWidth(g []string) int {
	w := 0
	for _, l := range g {
		if n := utf8.RuneCountInString(l); n > w {
			w = n
		}
	}
	return w
}

func styleLogoRow(s string) string {
	block := lipgloss.NewStyle().Foreground(ColText).Bold(true)
	shadow := lipgloss.NewStyle().Foreground(ColShadow)
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '█', '▀', '▄':
			sb.WriteString(block.Render(string(r)))
		case ' ':
			sb.WriteRune(' ')
		default:
			sb.WriteString(shadow.Render(string(r)))
		}
	}
	return sb.String()
}

func init() {
	for r := 0; r < logoRows; r++ {
		var sb strings.Builder
		for _, ch := range logoText {
			if ch == ' ' {
				sb.WriteString("   ")
				continue
			}
			g := glyphs[ch]
			w := glyphWidth(g)
			line := g[r]
			sb.WriteString(line + spaces(w-utf8.RuneCountInString(line)))
		}
		logoPlain[r] = sb.String()
		logoStyled[r] = styleLogoRow(logoPlain[r])
	}
	LogoWidth = utf8.RuneCountInString(logoPlain[0])
}

// Banner returns the app name in bold with the version tag beside it.
func Banner(version string) string {
	ver := MutedStyle.Render("v" + version)
	return TextStyle.Bold(true).Render("KAI CODE") + " " + ver
}
