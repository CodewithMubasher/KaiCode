package components

import "strings"

// MaxPaletteRows is the most rows the slash-command popup shows at once.
const MaxPaletteRows = 10

// Command is one entry in the slash-command popup.
type Command struct {
	Name string // without the leading slash, e.g. "clear"
	Desc string
}

// DefaultCommands is the built-in slash-command list (kept alphabetical).
func DefaultCommands() []Command {
	return []Command{
		{"agents", "Switch agent"},
		{"btw", "One-shot answer from the session's context without adding to the conversation"},
		{"cd", "Change working directory"},
		{"clear", "Clear session"},
		{"compact", "Compact session"},
		{"connect", "Connect an integration"},
		{"continue", "Switch session"},
		{"copy", "Copy last assistant message"},
		{"debug", "View debug info"},
		{"diff", "Open diff viewer"},
		{"exit", "Quit Kai"},
		{"help", "Show help"},
		{"init", "Generate the system prompt for this folder"},
		{"model", "Switch web chat model"},
		{"new", "Start a new session"},
	}
}

// PaletteModel is the slash-command popup state: which commands match the
// text typed after "/", which one is highlighted, and the scroll window.
type PaletteModel struct {
	all       []Command
	matches   []Command
	sel       int
	offset    int
	rows      int    // visible rows (set by the layout)
	query     string // text after the slash
	dismissed string // input value the user closed the popup on (Esc)
}

func NewPalette(cmds []Command) PaletteModel {
	p := PaletteModel{all: cmds, rows: MaxPaletteRows}
	p.matches = cmds
	return p
}

// SetRows sets how many rows fit on screen, keeping the selection visible.
func (p PaletteModel) SetRows(n int) PaletteModel {
	if n < 1 {
		n = 1
	}
	if n > MaxPaletteRows {
		n = MaxPaletteRows
	}
	p.rows = n
	return p.clamp()
}

// Visible reports whether the popup should be drawn.
func (p PaletteModel) Visible() bool { return len(p.matches) > 0 }

// Sync updates the popup from the current input text. The popup is active
// while the input is a single "/word" token (no space, no newline).
func (p PaletteModel) Sync(value string) PaletteModel {
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \n") || value == p.dismissed {
		p.matches = nil
		if value != p.dismissed {
			p.dismissed = ""
		}
		p.sel, p.offset, p.query = 0, 0, ""
		return p
	}
	q := strings.ToLower(value[1:])
	if q != p.query || len(p.matches) == 0 {
		p.query = q
		p.matches = filterCommands(p.all, q)
		p.sel, p.offset = 0, 0
	}
	return p
}

// Dismiss hides the popup until the input text changes (Esc).
func (p PaletteModel) Dismiss(value string) PaletteModel {
	p.dismissed = value
	p.matches = nil
	p.sel, p.offset = 0, 0
	return p
}

func (p PaletteModel) Up() PaletteModel {
	if len(p.matches) == 0 {
		return p
	}
	p.sel--
	if p.sel < 0 {
		p.sel = len(p.matches) - 1
	}
	return p.clamp()
}

func (p PaletteModel) Down() PaletteModel {
	if len(p.matches) == 0 {
		return p
	}
	p.sel++
	if p.sel >= len(p.matches) {
		p.sel = 0
	}
	return p.clamp()
}

// Selected returns the highlighted command.
func (p PaletteModel) Selected() (Command, bool) {
	if p.sel < 0 || p.sel >= len(p.matches) {
		return Command{}, false
	}
	return p.matches[p.sel], true
}

// clamp scrolls the window so the selected row stays visible.
func (p PaletteModel) clamp() PaletteModel {
	if p.sel < p.offset {
		p.offset = p.sel
	}
	if p.sel >= p.offset+p.rows {
		p.offset = p.sel - p.rows + 1
	}
	if max := len(p.matches) - p.rows; p.offset > max {
		p.offset = max
	}
	if p.offset < 0 {
		p.offset = 0
	}
	return p
}

// filterCommands keeps commands whose name starts with q first, then those
// that merely contain it (in name or description).
func filterCommands(all []Command, q string) []Command {
	if q == "" {
		return all
	}
	var prefix, rest []Command
	for _, c := range all {
		name := strings.ToLower(c.Name)
		switch {
		case strings.HasPrefix(name, q):
			prefix = append(prefix, c)
		case strings.Contains(name, q), strings.Contains(strings.ToLower(c.Desc), q):
			rest = append(rest, c)
		}
	}
	return append(prefix, rest...)
}
