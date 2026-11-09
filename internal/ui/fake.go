package ui

import (
	"time"

	"kai/internal/ui/components"
)

type fakeKind int

const (
	fakeThink fakeKind = iota
	fakeRead
	fakeShell
	fakeEdit
)

type fakeItem struct {
	kind  fakeKind
	text  string // thinking text, file path (read/edit), or shell command
	out   string // fakeShell output
	diff  []components.DiffLine
	runes []rune
}

const fakeAnswer = `## Done

Moved the home screen up and tightened the command popup.

- **Home position** — ` + "`Position(0.45)`" + ` → ` + "`Position(0.40)`" + ` in ` + "`app.go`" + `
- **Popup size** — ` + "`MaxPaletteRows`" + ` lowered from 10 to 8
- **Keys** — added a ` + "`Complete`" + ` binding for ` + "`tab`" + `

All packages pass with ` + "`go test ./...`" + `.

This is a placeholder reply; Phase 2 connects the real agent loop.`

const (
	ctx = components.DiffContext
	add = components.DiffAdd
	del = components.DiffDel
)

// fakeStream feeds a canned scenario into the chat in small chunks, standing
// in for a real streaming model until the agent loop exists:
//
//	3 thinking blocks, 5 reads, 2 shell commands, 3 edits, markdown summary
type fakeStream struct {
	script      []fakeItem
	idx, pos    int
	segStart    time.Time
	answer      []rune
	apos        int
	answerEnded bool
}

func think(s string) fakeItem { return fakeItem{kind: fakeThink, text: s} }
func read(p string) fakeItem  { return fakeItem{kind: fakeRead, text: p} }
func sh(cmd, out string) fakeItem {
	return fakeItem{kind: fakeShell, text: cmd, out: out}
}
func edit(p string, d ...components.DiffLine) fakeItem {
	return fakeItem{kind: fakeEdit, text: p, diff: d}
}

func newFakeStream() *fakeStream {
	items := []fakeItem{
		think("The home screen sits too low and the slash popup should stay compact. " +
			"I need to find where the home block is placed, so I will read app.go and the logo first."),
		read(`internal\ui\app.go`),
		read(`internal\ui\components\logo.go`),
		sh(`grep -n "Position" internal/ui/app.go`,
			`205:    content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Position(0.45), block)`),
		think("The vertical position is a single constant at line 205. " +
			"The popup rows are capped in the palette, and the keymap needs a complete key. " +
			"Let me check how the input and palette use these before changing them."),
		read(`internal\ui\components\input.go`),
		edit(`internal\ui\app.go`,
			components.DiffLine{Kind: ctx, Num: 201, Text: "components.Banner(version.Version),"},
			components.DiffLine{Kind: ctx, Num: 202, Text: `"",`},
			components.DiffLine{Kind: ctx, Num: 203, Text: "m.input.View(),"},
			components.DiffLine{Kind: ctx, Num: 204, Text: ")"},
			components.DiffLine{Kind: del, Num: 205, Text: "content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Position(0.45), block)"},
			components.DiffLine{Kind: add, Num: 205, Text: "content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Position(0.40), block)"},
			components.DiffLine{Kind: ctx, Num: 206, Text: "} else {"},
			components.DiffLine{Kind: ctx, Num: 207, Text: `gap := strings.Repeat(" ", m.mainW)`},
			components.DiffLine{Kind: ctx, Num: 208, Text: "mainCol := lipgloss.JoinVertical(lipgloss.Left,"},
			components.DiffLine{Kind: ctx, Num: 209, Text: "m.chat.View(),"},
		),
		read(`internal\ui\components\palette.go`),
		edit(`internal\ui\components\palette.go`,
			components.DiffLine{Kind: ctx, Num: 4, Text: "// MaxPaletteRows is the most rows the slash-command popup shows at once."},
			components.DiffLine{Kind: del, Num: 5, Text: "const MaxPaletteRows = 10"},
			components.DiffLine{Kind: add, Num: 5, Text: "const MaxPaletteRows = 8"},
			components.DiffLine{Kind: ctx, Num: 6, Text: ""},
			components.DiffLine{Kind: ctx, Num: 7, Text: "// Command is one entry in the slash-command popup."},
		),
		read(`internal\ui\keymap.go`),
		edit(`internal\ui\keymap.go`,
			components.DiffLine{Kind: ctx, Num: 7, Text: "Newline key.Binding"},
			components.DiffLine{Kind: add, Num: 8, Text: "Complete key.Binding"},
			components.DiffLine{Kind: ctx, Num: 9, Text: "Clear   key.Binding"},
			components.DiffLine{Kind: ctx, Num: 10, Text: "}"},
		),
		sh("go test ./...",
			"?     kai/cmd/kai [no test files]\n"+
				"ok    kai/internal/ui 0.412s\n"+
				"ok    kai/internal/ui/components 0.318s"),
		think("All three edits are in and the tests pass. " +
			"Nothing else depends on the old values, so I can write a short summary."),
	}
	for i := range items {
		items[i].runes = []rune(items[i].text)
	}
	return &fakeStream{script: items, answer: []rune(fakeAnswer)}
}

func chunkEnd(pos, n, total int) int {
	if pos+n > total {
		return total
	}
	return pos + n
}

func (f *fakeStream) step(c components.ChatModel) components.ChatModel {
	const chunk = 4

	if f.idx < len(f.script) {
		it := f.script[f.idx]
		switch it.kind {
		case fakeThink:
			if f.pos == 0 {
				f.segStart = time.Now()
			}
			if f.pos < len(it.runes) {
				end := chunkEnd(f.pos, chunk, len(it.runes))
				c = c.StreamThinking(string(it.runes[f.pos:end]))
				f.pos = end
				return c
			}
			c = c.EndThinking(time.Since(f.segStart))
		case fakeRead:
			c = c.ReadFile(it.text)
		case fakeShell:
			c = c.RunShell(it.text, it.out)
		case fakeEdit:
			c = c.EditFile(it.text, it.diff)
		}
		f.idx++
		f.pos = 0
		return c
	}

	if f.apos < len(f.answer) {
		end := chunkEnd(f.apos, chunk, len(f.answer))
		c = c.StreamAnswer(string(f.answer[f.apos:end]))
		f.apos = end
	} else if !f.answerEnded {
		c = c.EndAnswer()
		f.answerEnded = true
	}
	return c
}
