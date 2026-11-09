package components

import (
"strings"

tea "charm.land/bubbletea/v2"
"charm.land/lipgloss/v2"
)

const maxInputRows = 6

type InputModel struct {
width       int
value       []rune
cursor      int
placeholder string
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

func (i InputModel) Value() string { return string(i.value) }

// Width is the full rendered width of the input.
func (i InputModel) Width() int { return i.width }

// SetValue replaces the text and puts the cursor at the end.
func (i InputModel) SetValue(s string) InputModel {
i.value = []rune(s)
i.cursor = len(i.value)
return i
}

func (i InputModel) Reset() InputModel {
i.value = nil
i.cursor = 0
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

func (i InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
k, ok := msg.(tea.KeyPressMsg)
if !ok {
return i, nil
}
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
i = i.insert([]rune(k.Text))
}
}
return i, nil
}

func (i InputModel) contentW() int {
n := i.width - 2*Margin
if n < 10 {
n = 10
}
return n
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
before := string(i.value[:i.cursor])
c, after := " ", ""
if i.cursor < len(i.value) {
if i.value[i.cursor] == '\n' {
after = string(i.value[i.cursor:])
} else {
c = string(i.value[i.cursor])
after = string(i.value[i.cursor+1:])
}
}
text = tx.Render(before) + cur.Render(c) + tx.Render(after)
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
