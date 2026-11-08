package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const maxInputRows = 6

const (
	// Pastes of pillMinLines lines or more (that is, more than 5) are shown
	// as a "Pasted N Lines" pill instead of their text.
	pillMinLines = 6
	// A pill sits in the value as one private-use rune, pillBase + index
	// into pastes, so the cursor and backspace treat it as a single unit.
	pillBase = 0xE000
)

type InputModel struct {
	width       int
	value       []rune
	cursor      int
	placeholder string
	pastes      []string // full text behind each pill
}

func NewInput(w int) InputModel {
	return InputModel{width: w, placeholder: "Type a message..."}
}

func (i InputModel) SetPlaceholder(p string) InputModel {
	i.placeholder = p
	return i
}

func (i InputModel) SetWidth(w int) InputModel {
	i.width = w
	return i
}

// Width is the full rendered width of the input.
func (i InputModel) Width() int { return i.width }

// pill reports whether r is a paste pill and which paste it stands for.
func (i InputModel) pill(r rune) (int, bool) {
	idx := int(r) - pillBase
	if idx >= 0 && idx < len(i.pastes) {
		return idx, true
	}
	return 0, false
}

// Value is the text with every pill expanded to what was really pasted.
func (i InputModel) Value() string {
	var b strings.Builder
	for _, r := range i.value {
		if idx, ok := i.pill(r); ok {
			b.WriteString(i.pastes[idx])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SetValue replaces the text and puts the cursor at the end.
func (i InputModel) SetValue(s string) InputModel {
	i.value = []rune(s)
	i.cursor = len(i.value)
	i.pastes = nil
	return i
}

func (i InputModel) Reset() InputModel {
	i.value = nil
	i.cursor = 0
	i.pastes = nil
	return i
}

func (i InputModel) insert(rs []rune) InputModel {
	nv := make([]rune, 0, len(i.value)+len(rs))
	nv = append(nv, i.value[:i.cursor]...)
	nv = append(nv, rs...)
	nv = append(nv, i.value[i.cursor:]...)
	i.value = nv
	i.cursor += len(rs)
	return i
}

func pasteLines(s string) int {
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

// Paste inserts pasted text at the cursor. Long pastes become a pill.
func (i InputModel) Paste(s string) InputModel {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	if s == "" {
		return i
	}
	if pasteLines(s) >= pillMinLines {
		pastes := append(append([]string(nil), i.pastes...), s)
		i.pastes = pastes
		return i.insert([]rune{rune(pillBase + len(pastes) - 1)})
	}
	return i.insert([]rune(s))
}

func (i InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.PasteMsg:
		return i.Paste(m.Content), nil
	case tea.KeyPressMsg:
		return i.key(m), nil
	}
	return i, nil
}

func (i InputModel) key(k tea.KeyPressMsg) InputModel {
	switch k.String() {
	case "backspace":
		if i.cursor > 0 {
			nv := append([]rune{}, i.value[:i.cursor-1]...)
			i.value = append(nv, i.value[i.cursor:]...)
			i.cursor--
		}
	case "delete":
		if i.cursor < len(i.value) {
			nv := append([]rune{}, i.value[:i.cursor]...)
			i.value = append(nv, i.value[i.cursor+1:]...)
		}
	case "left":
		if i.cursor > 0 {
			i.cursor--
		}
	case "right":
		if i.cursor < len(i.value) {
			i.cursor++
		}
	case "home":
		i.cursor = 0
	case "end":
		i.cursor = len(i.value)
	case "shift+enter", "ctrl+j", "alt+enter":
		i = i.insert([]rune{'\n'})
	default:
		if k.Text != "" {
			var typed []rune
			for _, r := range k.Text {
				if r < pillBase || r > pillBase+0x0fff { // never let typing forge a pill
					typed = append(typed, r)
				}
			}
			i = i.insert(typed)
		}
	}
	return i
}

func (i InputModel) contentW() int {
	n := i.width - 2*Margin
	if n < 10 {
		n = 10
	}
	return n
}

// render styles runes with tx, drawing pills as teal "Pasted N Lines" chips.
func (i InputModel) render(rs []rune, tx lipgloss.Style) string {
	chip := lipgloss.NewStyle().Background(ColTeal).Foreground(ColOnTeal).Bold(true)
	var b strings.Builder
	start := 0
	for n, r := range rs {
		if idx, ok := i.pill(r); ok {
			b.WriteString(tx.Render(string(rs[start:n])))
			b.WriteString(chip.Render(fmt.Sprintf(" Pasted %d Lines ", pasteLines(i.pastes[idx]))))
			start = n + 1
		}
	}
	b.WriteString(tx.Render(string(rs[start:])))
	return b.String()
}

func (i InputModel) block() []string {
	tx := lipgloss.NewStyle().Background(ColPanel).Foreground(ColText)
	cur := tx.Reverse(true)
	muted := lipgloss.NewStyle().Background(ColPanel).Foreground(ColMuted)

	var text string
	if len(i.value) == 0 {
		ph := []rune(i.placeholder)
		if len(ph) == 0 {
			ph = []rune(" ")
		}
		text = cur.Render(string(ph[0])) + muted.Render(string(ph[1:]))
	} else {
		c, after := " ", []rune(nil)
		if i.cursor < len(i.value) {
			r := i.value[i.cursor]
			if _, isPill := i.pill(r); r == '\n' || isPill {
				after = i.value[i.cursor:]
			} else {
				c = string(r)
				after = i.value[i.cursor+1:]
			}
		}
		text = i.render(i.value[:i.cursor], tx) + cur.Render(c) + i.render(after, tx)
	}
	return panelBlock(text, i.contentW(), "", "", maxInputRows)
}

func (i InputModel) Height() int { return len(i.block()) }

func (i InputModel) View() string {
	cw := i.contentW()
	lines := i.block()
	rows := make([]string, len(lines))
	for n, l := range lines {
		rows[n] = spaces(Margin) + l + spaces(i.width-cw-Margin)
	}
	return strings.Join(rows, "\n")
}
