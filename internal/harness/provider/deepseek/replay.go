package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"kai/internal/harness"
)

// Replay is a Provider that answers from recorded raw SSE bodies, one per
// Send, in order. It lets the whole loop be tested with real captured output
// and no browser.
type Replay struct {
	mu    sync.Mutex
	turns [][]byte
	next  int
}

func NewReplay(turns ...[]byte) *Replay { return &Replay{turns: turns} }

// NewReplayFiles loads one recorded SSE body per file.
func NewReplayFiles(paths ...string) (*Replay, error) {
	var turns [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		turns = append(turns, b)
	}
	return NewReplay(turns...), nil
}

func (r *Replay) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next >= len(r.turns) {
		return nil, errors.New("replay: no more recorded replies")
	}
	b := r.turns[r.next]
	r.next++
	return Decode(ctx, bytes.NewReader(b)), nil
}

// BuildSSE makes an SSE body in the same shape the web chat produces, from
// text chunks. Handy for tests and demos.
func BuildSSE(chunks ...string) []byte {
	var b bytes.Buffer
	b.WriteString("event: ready\ndata: {\"request_message_id\":1,\"response_message_id\":2,\"model_type\":\"default\"}\n\n")
	for i, c := range chunks {
		j, _ := json.Marshal(c)
		switch i {
		case 0:
			fmt.Fprintf(&b, "data: {\"v\":{\"response\":{\"message_id\":2,\"status\":\"WIP\",\"fragments\":[{\"id\":2,\"type\":\"RESPONSE\",\"content\":%s}]}}}\n\n", j)
		case 1:
			fmt.Fprintf(&b, "data: {\"p\":\"response/fragments/-1/content\",\"o\":\"APPEND\",\"v\":%s}\n\n", j)
		default:
			fmt.Fprintf(&b, "data: {\"v\":%s}\n\n", j)
		}
	}
	b.WriteString("data: {\"p\":\"response/status\",\"o\":\"SET\",\"v\":\"FINISHED\"}\n\n")
	b.WriteString("event: close\ndata: {\"click_behavior\":\"none\",\"auto_resume\":false}\n\n")
	return b.Bytes()
}
