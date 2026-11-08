package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// DialogItem is one action in the command dialog.
type DialogItem struct {
	Group string // section heading, e.g. "Suggested"
	Title string
	Key   string // shortcut hint shown on the right, e.g. "ctrl+n"
	ID    string // what the app runs when it is chosen
}

// DefaultDialogItems is the built-in action list.
func DefaultDialogItems() []DialogItem {
	return []DialogItem{
		{"Suggested", "New session", "ctrl+n", "new"},
		{"Suggested", "Copy last response", "/copy", "copy"},
		{"Suggested", "Toggle sidebar", "ctrl+d", "sidebar"},
		{"Suggested", "Switch model", "", "model"},

		{"Session", "Switch session", "/continue", "sessions"},
		{"Session", "Compact session", "/compact", "compact"},
		{"Session", "Clear chat", "ctrl+l", "clear"},

		{"Prompt", "Paste from clipboard", "ctrl+v", "paste"},

		{"App", "Open settings", "", "settings"},
		{"App", "Quit", "ctrl+c", "exit"},
	}
}

// DialogResult tells the app what a key press did to the dialog.
type DialogResult int

const (
	DialogNone   DialogResult = iota // still open
	DialogClosed                     // closed with esc
	DialogChose                      // closed with enter; see Selected
)

// DialogModel is a centered modal listing actions in groups, with a search
// box on top. Typing filters, up/down moves, enter chooses, esc closes.
type DialogModel struct {
	items    []DialogItem
	open     bool
	query    string
	matches  []int // indexes into items, in display order
	sel      int   // index into matches
	offset   int   // first visible display line
	w, listH int
}

func NewDialog(items []DialogItem) DialogModel {
	return DialogModel{items: items}
}

func (d DialogModel) Visible() bool { return d.open }

// Open shows the dialog with an empty search.
func (d DialogModel) Open() DialogModel {
	d.open, d.query, d.sel, d.offset = true, "", 0, 0
	return d.refilter()
}

func (d DialogModel) Close() DialogModel {
	d.open = false
	return d
}

// SetSize fits the dialog to the terminal size.
func (d DialogModel) SetSize(termW, termH int) DialogModel {
	d.w = termW - 4
	if d.w > 64 {
		d.w = 64
	}
	if d.w < 30 {
		d.w = 30
	}
	maxH := termH - 2
	if maxH > 24 {
		maxH = 24
	}
	d.listH = maxH - 6 // blank, title, blank, search, blank, ..., blank
	if d.listH < 3 {
		d.listH = 3
	}
	return d.clamp()
}

// Selected returns the highlighted action (still valid right after enter).
func (d DialogModel) Selected() (DialogItem, bool) {
	if d.sel < 0 || d.sel >= len(d.matches) {
		return DialogItem{}, false
	}
	return d.items[d.matches[d.sel]], true
}

// refilter rebuilds matches: grouped in list order when the search is
// empty, otherwise a flat list with name-prefix matches first.
func (d DialogModel) refilter() DialogModel {
	d.matches = nil
	q := strings.ToLower(strings.TrimSpace(d.query))
	if q == "" {
		var groups []string
		for _, it := range d.items {
			if !contains(groups, it.Group) {
				groups = append(groups, it.Group)
			}
		}
		for _, g := range groups {
			for n, it := range d.items {
				if it.Group == g {
					d.matches = append(d.matches, n)
				}
			}
		}
	} else {
		var rest []int
		for n, it := range d.items {
			t := strings.ToLower(it.Title)
			switch {
			case strings.HasPrefix(t, q):
				d.matches = append(d.matches, n)
			case strings.Contains(t, q), strings.Contains(strings.ToLower(it.Key), q):
				rest = append(rest, n)
			}
		}
		d.matches = append(d.matches, rest...)
	}
	d.sel, d.offset = 0, 0
	return d
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// dline is one display line: a group heading (item -1) or an action.
type dline struct {
	head string
	item int // index into matches
}

func (d DialogModel) lines() []dline {
	var out []dline
	grouped := strings.TrimSpace(d.query) == ""
	last := ""
	for n, mi := range d.matches {
		if g := d.items[mi].Group; grouped && g != last {
			if last != "" {
				out = append(out, dline{item: -2}) // blank between groups
			}
			out = append(out, dline{head: g, item: -1})
			last = g
		}
		out = append(out, dline{item: n})
	}
	return out
}

// selLine is the display line of the selected action.
func (d DialogModel) selLine(ls []dline) int {
	for i, l := range ls {
		if l.item == d.sel {
			return i
		}
	}
	return 0
}

// clamp scrolls so the selected line (and its group heading) is visible.
func (d DialogModel) clamp() DialogModel {
	ls := d.lines()
	i := d.selLine(ls)
	top := i
	if i > 0 && ls[i-1].item == -1 {
		top = i - 1 // keep the heading above the first item of a group
	}
	if top < d.offset {
		d.offset = top
	}
	if i >= d.offset+d.listH {
		d.offset = i - d.listH + 1
	}
	if m := len(ls) - d.listH; d.offset > m {
		d.offset = m
	}
	if d.offset < 0 {
		d.offset = 0
	}
	return d
}

func (d DialogModel) move(delta int) DialogModel {
	if n := len(d.matches); n > 0 {
		d.sel = (d.sel + delta + n) % n
	}
	return d.clamp()
}

// Update handles a key press while the dialog is open.
func (d DialogModel) Update(k tea.KeyPressMsg) (DialogModel, DialogResult) {
	switch k.String() {
	case "esc":
		return d.Close(), DialogClosed
	case "enter":
		if _, ok := d.Selected(); ok {
			return d.Close(), DialogChose
		}
	case "up", "ctrl+p":
		d = d.move(-1)
	case "down":
		d = d.move(1)
	case "backspace":
		if rs := []rune(d.query); len(rs) > 0 {
			d.query = string(rs[:len(rs)-1])
			d = d.refilter().clamp()
		}
	case "ctrl+u":
		d.query = ""
		d = d.refilter().clamp()
	default:
		if k.Text != "" {
			d.query += k.Text
			d = d.refilter().clamp()
		}
	}
	return d, DialogNone
}

// Size is the box's width and height in cells.
func (d DialogModel) Size() (w, h int) {
	rows := len(d.lines())
	if rows > d.listH {
		rows = d.listH
	}
	if rows == 0 {
		rows = 1
	}
	return d.w, rows + 6
}

// View renders the box: one string per row, all d.w cells wide.
func (d DialogModel) View() []string {
	pw := d.w
	if !d.open || pw < 12 {
		return nil
	}
	bg := lipgloss.NewStyle().Background(ColPanel)
	text := bg.Foreground(ColText)
	muted := bg.Foreground(ColMuted)
	pad := bg.Render("  ")
	inner := pw - 4
	blank := bg.Render(spaces(pw))

	// title row: "Commands" ........ "esc"
	title := text.Bold(true).Render("Commands")
	esc := muted.Render("esc")
	gap := bg.Render(spaces(inner - lipgloss.Width("Commands") - lipgloss.Width("esc")))
	rows := []string{blank, pad + title + gap + esc + pad, blank}

	// search row
	cur := text.Reverse(true)
	var search string
	if d.query == "" {
		search = cur.Render("S") + muted.Render("earch")
	} else {
		q := ansi.Truncate(d.query, inner-1, "")
		search = text.Render(q) + cur.Render(" ")
	}
	rows = append(rows, pad+padStyled(search, inner, bg)+pad, blank)

	// list
	ls := d.lines()
	end := d.offset + d.listH
	if end > len(ls) {
		end = len(ls)
	}
	if len(ls) == 0 {
		rows = append(rows, pad+padStyled(muted.Render("No results"), inner, bg)+pad)
	}
	for _, l := range ls[min(d.offset, len(ls)):end] {
		switch {
		case l.item == -2:
			rows = append(rows, blank)
		case l.item == -1:
			rows = append(rows, pad+padStyled(bg.Foreground(ColAccent).Bold(true).Render(l.head), inner, bg)+pad)
		default:
			rows = append(rows, d.itemRow(d.items[d.matches[l.item]], l.item == d.sel, pw))
		}
	}
	return append(rows, blank)
}

// itemRow renders "  Title            key  ", highlighted in teal if selected.
func (d DialogModel) itemRow(it DialogItem, selected bool, pw int) string {
	region := pw - 2 // one cell of margin on each side of the highlight
	keyW := lipgloss.Width(it.Key)
	titleW := region - 2 - keyW
	if keyW > 0 {
		titleW-- // gap before the key
	}
	title := ansi.Truncate(it.Title, titleW, "…")

	st, kst := lipgloss.NewStyle().Background(ColPanel).Foreground(ColText), lipgloss.NewStyle().Background(ColPanel).Foreground(ColMuted)
	if selected {
		st = lipgloss.NewStyle().Background(ColTeal).Foreground(ColOnTeal).Bold(true)
		kst = lipgloss.NewStyle().Background(ColTeal).Foreground(ColOnTeal)
	}
	body := st.Render(" "+padRight(title, titleW+boolInt(keyW > 0))) + kst.Render(it.Key) + st.Render(" ")
	edge := lipgloss.NewStyle().Background(ColPanel).Render(" ")
	return edge + body + edge
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// padStyled pads styled content to w cells with the given background.
func padStyled(s string, w int, bg lipgloss.Style) string {
	return s + bg.Render(spaces(w-lipgloss.Width(s)))
}
