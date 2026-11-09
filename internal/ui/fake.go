package ui

import (
	"time"

	"kai/internal/ui/components"
)

type fakeKind int

const (
	fakeThink fakeKind = iota
	fakeRead
)

type fakeItem struct {
	kind  fakeKind
	text  string // thinking text, or file path for fakeRead
	runes []rune
}

const fakeAnswer = "I read app.tsx and it already handles this. " +
	"This is a placeholder response; Phase 2 will connect the real agent loop " +
	"and this text will stream in from the model the same way."

// fakeStream feeds a canned scenario into the chat in small chunks, standing
// in for a real streaming model until the agent loop exists:
//
//	thinking -> Read file (app.tsx) -> thinking -> answer
type fakeStream struct {
	script      []fakeItem
	idx, pos    int
	segStart    time.Time
	answer      []rune
	apos        int
	answerEnded bool
}

func newFakeStream() *fakeStream {
	items := []fakeItem{
		{kind: fakeThink, text: "The user wants help with their app. Before answering I should look at the " +
			"entry file to see how things are wired together.\n\n" +
			"app.tsx is the most likely starting point, so I will read it first."},
		{kind: fakeRead, text: "app.tsx"},
		{kind: fakeThink, text: "Now I have the file contents. The component structure is simple and " +
			"the state lives at the top level, so the answer can be short and direct.\n\n" +
			"No other files are needed. Time to write the response."},
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
