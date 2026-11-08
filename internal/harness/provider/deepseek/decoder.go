package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"kai/internal/harness"
)

// The web chat streams a reply as a small JSON-patch protocol:
//
//	event: ready      {"request_message_id":1,"response_message_id":2}
//	data: {"v":{"response":{... "fragments":[{"type":"RESPONSE","content":"Hello"}]}}}
//	data: {"p":"response/fragments/-1/content","o":"APPEND","v":"!"}
//	data: {"v":" How"}                      <- no path: same as the previous one
//	data: {"p":"response","o":"BATCH","v":[...]}
//	data: {"p":"response/status","o":"SET","v":"FINISHED"}
//	event: title      {"content":"Hello greeting"}
//	event: close      {...}
//
// Decoder turns that into StreamEvents. Fragments of type THINK (reasoning)
// and RESPONSE (the answer) are forwarded; other fragment types are ignored.

type patch struct {
	P string          `json:"p"`
	O string          `json:"o"`
	V json.RawMessage `json:"v"`
}

type fragmentJSON struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Decoder holds the state of one reply.
type Decoder struct {
	frags   []string // fragment types, in order
	lastIdx int      // fragment the next bare {"v":"..."} appends to (-1 = last)
	ended   bool
}

func NewDecoder() *Decoder { return &Decoder{lastIdx: -1} }

// Handle processes one SSE event and returns the stream events it produced.
func (d *Decoder) Handle(event, data string) []harness.StreamEvent {
	data = strings.TrimSpace(data)
	switch event {
	case "ready":
		var r struct {
			ResponseMessageID int `json:"response_message_id"`
		}
		if json.Unmarshal([]byte(data), &r) == nil && r.ResponseMessageID != 0 {
			return []harness.StreamEvent{{Kind: harness.StreamReady, MessageID: r.ResponseMessageID}}
		}
		return nil
	case "title":
		var t struct {
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(data), &t) == nil && t.Content != "" {
			return []harness.StreamEvent{{Kind: harness.StreamTitle, Text: t.Content}}
		}
		return nil
	case "error":
		if d.ended {
			return nil
		}
		d.ended = true
		return []harness.StreamEvent{{Kind: harness.StreamError, Err: errors.New("deepseek error: " + data)}}
	case "close":
		if d.ended {
			return nil
		}
		d.ended = true
		return []harness.StreamEvent{{Kind: harness.StreamError, Err: errors.New("the connection closed before the reply was finished")}}
	case "", "message":
		var p patch
		if json.Unmarshal([]byte(data), &p) != nil {
			return nil
		}
		return d.apply(p.P, p.O, p.V)
	}
	return nil // update_session, hint, ... : nothing we need
}

func asString(v json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

func (d *Decoder) apply(path, op string, v json.RawMessage) []harness.StreamEvent {
	switch {
	case path == "" && op == "":
		// A bare string continues the previous APPEND.
		if s, ok := asString(v); ok {
			return d.appendContent(d.lastIdx, s)
		}
		// Otherwise it is the opening snapshot of the reply.
		var snap struct {
			Response *struct {
				Fragments []fragmentJSON `json:"fragments"`
			} `json:"response"`
		}
		if json.Unmarshal(v, &snap) != nil || snap.Response == nil {
			return nil
		}
		var out []harness.StreamEvent
		for _, f := range snap.Response.Fragments {
			out = append(out, d.addFragment(f)...)
		}
		return out

	case path == "response" && op == "BATCH":
		var subs []patch
		if json.Unmarshal(v, &subs) != nil {
			return nil
		}
		var out []harness.StreamEvent
		for _, s := range subs {
			out = append(out, d.apply("response/"+s.P, s.O, s.V)...)
		}
		return out

	case path == "response/status":
		if s, ok := asString(v); ok {
			return d.status(s)
		}

	case path == "response/fragments" && (op == "APPEND" || op == ""):
		var fs []fragmentJSON
		if json.Unmarshal(v, &fs) != nil {
			return nil
		}
		var out []harness.StreamEvent
		for _, f := range fs {
			out = append(out, d.addFragment(f)...)
		}
		return out

	case strings.HasPrefix(path, "response/fragments/") && strings.HasSuffix(path, "/content"):
		mid := strings.TrimSuffix(strings.TrimPrefix(path, "response/fragments/"), "/content")
		idx, err := strconv.Atoi(mid)
		if err != nil {
			return nil
		}
		if s, ok := asString(v); ok && (op == "APPEND" || op == "") {
			d.lastIdx = idx
			return d.appendContent(idx, s)
		}
	}
	return nil
}

func (d *Decoder) addFragment(f fragmentJSON) []harness.StreamEvent {
	d.frags = append(d.frags, f.Type)
	d.lastIdx = -1
	return emit(f.Type, f.Content)
}

func (d *Decoder) appendContent(idx int, s string) []harness.StreamEvent {
	if idx < 0 {
		idx = len(d.frags) - 1
	}
	if idx < 0 || idx >= len(d.frags) {
		return nil
	}
	return emit(d.frags[idx], s)
}

func (d *Decoder) status(s string) []harness.StreamEvent {
	if s == "WIP" || d.ended {
		return nil
	}
	d.ended = true
	return []harness.StreamEvent{{Kind: harness.StreamEnd, Text: s}}
}

func emit(typ, s string) []harness.StreamEvent {
	if s == "" {
		return nil
	}
	t := strings.ToUpper(typ)
	switch {
	case strings.Contains(t, "THINK"):
		return []harness.StreamEvent{{Kind: harness.StreamThinking, Text: s}}
	case t == "RESPONSE":
		return []harness.StreamEvent{{Kind: harness.StreamText, Text: s}}
	}
	return nil
}

// Decode reads a raw SSE body and streams harness events. The channel closes
// when the body ends or ctx is cancelled. This is what the bridge feeds.
func Decode(ctx context.Context, r io.Reader) <-chan harness.StreamEvent {
	ch := make(chan harness.StreamEvent, 64)
	go func() {
		defer close(ch)
		send := func(e harness.StreamEvent) bool {
			select {
			case ch <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}

		d := NewDecoder()
		err := ReadSSE(r, func(event, data string) error {
			for _, e := range d.Handle(event, data) {
				if !send(e) {
					return ctx.Err()
				}
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && !d.ended {
			send(harness.StreamEvent{Kind: harness.StreamError, Err: err})
			return
		}
		if !d.ended {
			send(harness.StreamEvent{Kind: harness.StreamError, Err: errors.New("the stream ended before the reply was finished")})
		}
	}()
	return ch
}
