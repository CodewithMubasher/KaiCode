package components

import (
"fmt"
"strings"

tea "charm.land/bubbletea/v2"
"charm.land/lipgloss/v2"
"github.com/charmbracelet/x/ansi"
)

type FormField struct {
Label       string
Placeholder string
Value       string
Secret      bool
}

type FormResult int

const (
FormNone FormResult = iota
FormCancelled
FormSubmitted
)

type FormModel struct {
title  string
fields []FormField
values [][]rune
focus  int
open   bool
err    string
note   string
w      int
}

func NewForm(title string, fields []FormField) FormModel {
f := FormModel{title: title, fields: fields, open: true}
for _, fl := range fields {
f.values = append(f.values, []rune(fl.Value))
}
return f
}

func (f FormModel) Visible() bool { return f.open }

func (f FormModel) Close() FormModel {
f.open = false
return f
}

func (f FormModel) SetSize(termW, termH int) FormModel {
f.w = termW - 4
if f.w > 64 {
f.w = 64
}
if f.w < 30 {
f.w = 30
}
return f
}

func (f FormModel) SetError(s string) FormModel {
f.err = s
return f
}

func (f FormModel) SetNote(s string) FormModel {
f.note = s
return f
}

func (f FormModel) Values() []string {
out := make([]string, len(f.values))
for i, v := range f.values {
out[i] = strings.TrimSpace(string(v))
}
return out
}

func (f FormModel) add(s string) FormModel {
if len(f.values) == 0 {
return f
}
s = strings.NewReplacer("\r\n", "", "\n", "", "\r", "", "\t", " ").Replace(s)
if f.fields[f.focus].Secret {
s = strings.Join(strings.Fields(s), "")
}
if s == "" {
return f
}
vals := make([][]rune, len(f.values))
copy(vals, f.values)
vals[f.focus] = append(append([]rune(nil), f.values[f.focus]...), []rune(s)...)
f.values = vals
f.err = ""
return f
}

func (f FormModel) Paste(s string) FormModel { return f.add(s) }

func (f FormModel) move(delta int) FormModel {
if n := len(f.fields); n > 0 {
f.focus = (f.focus + delta + n) % n
}
return f
}

func (f FormModel) Update(k tea.KeyPressMsg) (FormModel, FormResult) {
switch k.String() {
case "esc":
return f.Close(), FormCancelled
case "ctrl+s":
return f, FormSubmitted
case "enter":
if f.focus >= len(f.fields)-1 {
return f, FormSubmitted
}
return f.move(1), FormNone
case "tab", "down":
return f.move(1), FormNone
case "shift+tab", "up":
return f.move(-1), FormNone
case "backspace":
if v := f.values[f.focus]; len(v) > 0 {
vals := make([][]rune, len(f.values))
copy(vals, f.values)
vals[f.focus] = append([]rune(nil), v[:len(v)-1]...)
f.values = vals
f.err = ""
}
case "ctrl+u":
vals := make([][]rune, len(f.values))
copy(vals, f.values)
vals[f.focus] = nil
f.values = vals
default:
if k.Text != "" {
f = f.add(k.Text)
}
}
return f, FormNone
}

func (f FormModel) Size() (w, h int) { return f.w, len(f.View()) }

func (f FormModel) cell(i, inner int, bg lipgloss.Style) string {
text := bg.Foreground(ColText)
muted := bg.Foreground(ColMuted)
cur := text.Reverse(true)
v := f.values[i]
focused := i == f.focus

if len(v) == 0 {
ph := f.fields[i].Placeholder
if focused {
return cur.Render(" ") + muted.Render(ansi.Truncate(ph, inner-1, "…"))
}
return muted.Render(ansi.Truncate(ph, inner, "…"))
}
shown := string(v)
if f.fields[i].Secret {
shown = strings.Repeat("•", len(v))
}
if over := lipgloss.Width(shown) - (inner - 1); over > 0 {
shown = ansi.TruncateLeft(shown, over, "")
}
if focused {
return text.Render(shown) + cur.Render(" ")
}
return text.Render(shown)
}

func (f FormModel) View() []string {
pw := f.w
if !f.open || pw < 12 {
return nil
}
bg := lipgloss.NewStyle().Background(ColPanel)
text := bg.Foreground(ColText)
muted := bg.Foreground(ColMuted)
pad := bg.Render(spaces(dialogPadX))
inner := pw - 2*dialogPadX
blank := bg.Render(spaces(pw))
row := func(s string) string { return pad + padStyled(s, inner, bg) + pad }

var rows []string
for i := 0; i < dialogPadTop; i++ {
rows = append(rows, blank)
}
gap := bg.Render(spaces(inner - lipgloss.Width(f.title) - lipgloss.Width("esc")))

titleText := f.title
if len(f.fields) > 1 {
titleText = fmt.Sprintf("%s (Step %d/%d)", f.title, f.focus+1, len(f.fields))
}
rows = append(rows, pad+text.Bold(true).Render(titleText)+gap+muted.Render("esc")+pad, blank)

if f.focus < len(f.fields) {
fl := f.fields[f.focus]
label := bg.Foreground(ColTeal).Bold(true)
rows = append(rows, row(label.Render(fl.Label)))
rows = append(rows, blank)
rows = append(rows, row(f.cell(f.focus, inner, bg)))
}

rows = append(rows, blank)

switch {
case f.err != "":
rows = append(rows, row(bg.Foreground(ColError).Render(ansi.Truncate(f.err, inner, "…"))))
case f.note != "":
rows = append(rows, row(muted.Render(ansi.Truncate(f.note, inner, "…"))))
default:
rows = append(rows, blank)
}

instruction := "enter next · esc cancel"
if f.focus == len(f.fields)-1 {
instruction = "enter submit · esc cancel"
}
rows = append(rows, row(muted.Render(ansi.Truncate(instruction, inner, "…"))))

for i := 0; i < dialogPadBottom-1; i++ {
rows = append(rows, blank)
}
return rows
}
