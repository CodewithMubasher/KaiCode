package components

import (
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

type DiffKind int

const (
	DiffContext DiffKind = iota
	DiffAdd
	DiffDel
)

// DiffLine is one line of an edit: its file line number, text and whether
// it was kept, added or removed.
type DiffLine struct {
	Kind DiffKind
	Num  int
	Text string
}

var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// highlightCode colours one line of Go-ish code on top of the base style st.
func highlightCode(line string, st lipgloss.Style) string {
	rs := []rune(line)
	var b strings.Builder
	for i := 0; i < len(rs); {
		r := rs[i]
		j := i + 1
		switch {
		case unicode.IsSpace(r):
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				j++
			}
			b.WriteString(st.Render(string(rs[i:j])))
		case unicode.IsLetter(r) || r == '_':
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			w := string(rs[i:j])
			if goKeywords[w] {
				b.WriteString(st.Foreground(ColKeyword).Italic(true).Render(w))
			} else {
				b.WriteString(st.Foreground(ColCode).Render(w))
			}
		case unicode.IsDigit(r):
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			b.WriteString(st.Foreground(ColNumber).Render(string(rs[i:j])))
		case r == '"':
			for j < len(rs) && rs[j] != '"' {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(st.Foreground(ColCode).Render(string(rs[i:j])))
		default:
			b.WriteString(st.Foreground(ColPunct).Render(string(r)))
		}
		i = j
	}
	return b.String()
}

// editLines renders an edit step as a card with the same dim bar as shell:
//
//	▍
//	▍  ← Edit internal\ui\app.go
//	▍
//	▍   204    )
//	▍   205  - content = ...0.45...     (red row)
//	▍   205  + content = ...0.40...     (teal row)
//	▍
func editLines(path string, diff []DiffLine, w int) []string {
	const lead, tail = 2, 2
	pw := w - 1
	inner := pw - lead - tail
	if inner < 1 {
		inner = 1
	}
	bg := lipgloss.NewStyle().Background(ColPanel)
	blank := bg.Render(spaces(pw))

	numW := 3
	for _, d := range diff {
		if n := len(strconv.Itoa(d.Num)); n > numW {
			numW = n
		}
	}
	// region = pad + num + space + sign + space + code + pad
	codeW := inner - numW - 5
	if codeW < 4 {
		codeW = 4
	}

	head := hardWrap("← Edit "+path, inner)[0]
	out := []string{
		blank,
		bg.Render(spaces(lead)) + bg.Foreground(ColMuted).Bold(true).Render(padRight(head, inner)) + bg.Render(spaces(tail)),
	}
	if len(diff) > 0 {
		out = append(out, blank)
	}

	for _, d := range diff {
		rowBg, sign, signCol := ColPanel, " ", ColMuted
		switch d.Kind {
		case DiffAdd:
			rowBg, sign, signCol = ColAddBg, "+", ColAddSign
		case DiffDel:
			rowBg, sign, signCol = ColDelBg, "-", ColDelSign
		}
		st := lipgloss.NewStyle().Background(rowBg)
		for n, chunk := range hardWrap(strings.ReplaceAll(d.Text, "\t", "    "), codeW) {
			num, sg := spaces(numW), " "
			if n == 0 {
				num = spaces(numW-len(strconv.Itoa(d.Num))) + strconv.Itoa(d.Num)
				sg = sign
			}
			row := st.Render(" ") +
				st.Foreground(ColMuted).Render(num) +
				st.Render(" ") +
				st.Foreground(signCol).Bold(true).Render(sg) +
				st.Render(" ") +
				highlightCode(chunk, st) +
				st.Render(spaces(codeW-lipgloss.Width(chunk))+" ")
			out = append(out, bg.Render(spaces(lead))+row+bg.Render(spaces(tail)))
		}
	}
	return withBar(append(out, blank))
}
