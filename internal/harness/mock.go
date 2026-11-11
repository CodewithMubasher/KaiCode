package harness

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MockProvider answers from a script instead of a real model. It streams the
// reply in small chunks, which also exercises the parser's chunk handling.
type MockProvider struct {
	Script     func(turn int, t Turn) string
	ChunkRunes int           // runes per chunk (default 6)
	Delay      time.Duration // pause between chunks

	mu     sync.Mutex
	turn   int
	nextID int
}

func (m *MockProvider) Send(ctx context.Context, t Turn) (<-chan StreamEvent, error) {
	m.mu.Lock()
	n := m.turn
	m.turn++
	m.nextID += 2
	id := m.nextID
	m.mu.Unlock()

	reply := m.Script(n, t)
	size := m.ChunkRunes
	if size <= 0 {
		size = 6
	}

	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		send := func(e StreamEvent) bool {
			select {
			case ch <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(StreamEvent{Kind: StreamReady, MessageID: id}) {
			return
		}
		rs := []rune(reply)
		for i := 0; i < len(rs); i += size {
			end := i + size
			if end > len(rs) {
				end = len(rs)
			}
			if !send(StreamEvent{Kind: StreamText, Text: string(rs[i:end])}) {
				return
			}
			if m.Delay > 0 {
				select {
				case <-time.After(m.Delay):
				case <-ctx.Done():
					return
				}
			}
		}
		send(StreamEvent{Kind: StreamEnd, Text: "FINISHED"})
	}()
	return ch, nil
}

// DemoScript is a tiny scripted "model": it reads go.mod, then summarises it.
// It lets the UI run the real loop before the Chrome bridge exists.
func DemoScript(turn int, t Turn) string {
	if strings.HasPrefix(t.Prompt, "<tool_result") {
		return "Done. The project is a Go module named `kai`, built on Bubble Tea.\n\n" +
			"This reply came from the demo provider, so it is scripted: the loop, the DSML parsing and the " +
			"tool call you just saw were all real."
	}
	return "I'll read the module file first.\n" +
		"<|DSML|tool_calls>\n" +
		"<|DSML|invoke name=\"read_file\">\n" +
		"<|DSML|parameter name=\"path\" string=\"true\">go.mod</|DSML|parameter>\n" +
		"</|DSML|invoke>\n" +
		"</|DSML|tool_calls>"
}
