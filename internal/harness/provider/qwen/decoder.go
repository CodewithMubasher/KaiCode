// Package qwen decodes the server-sent-event stream of Qwen Chat
// (chat.qwen.ai) into harness events.
package qwen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"kai/internal/harness"
)

// Qwen Chat answers POST /api/v2/chat/completions?chat_id=<uuid> with plain
// "data:" events (no "event:" names), each one a complete JSON object:
//
//	data: {"response.created":{"chat_id":"..","parent_id":"..","response_id":".."}}
//	data: {"choices":[{"delta":{"role":"assistant","content":"Hello","phase":"answer","status":"typing"}}],"usage":{..}}
//	data: {"choices":[{"delta":{"content":"","role":"assistant","status":"finished","phase":"answer"}}]}
//
// Every delta carries a phase. "think" is the model's reasoning, "answer" is
// the reply; other phases (web search, summaries, ...) are ignored. A phase
// ends with status "finished", so only the "answer" phase finishing means the
// reply is complete: the thinking phase finishes first and the answer follows.
//
// A refused request (rate limit, not logged in, ...) does not come as events:
// it is one plain JSON body, {"success":false,"data":{"code":..,"details":..}},
// which Decode reports as an error with the server's own words.

type delta struct {
	Content json.RawMessage `json:"content"`
	Phase   string          `json:"phase"`
	Status  string          `json:"status"`
}

type chunk struct {
	Choices []struct {
		Delta delta `json:"delta"`
	} `json:"choices"`
	Usage   *usageWire      `json:"usage"`
	Error   json.RawMessage `json:"error"`
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
}

// usageWire is the token usage Qwen reports with a reply. It is the real
// count from the provider, not an estimate.
type usageWire struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

func (u usageWire) usage() (harness.Usage, bool) {
	p, c, t := u.PromptTokens, u.CompletionTokens, u.TotalTokens
	if p == 0 {
		p = u.InputTokens
	}
	if c == 0 {
		c = u.OutputTokens
	}
	if t == 0 {
		t = p + c
	}
	if t <= 0 {
		return harness.Usage{}, false
	}
	return harness.Usage{PromptTokens: p, CompletionTokens: c, TotalTokens: t}, true
}

// Decoder holds the state of one reply.
type Decoder struct {
	ended bool
}

func NewDecoder() *Decoder { return &Decoder{} }

// Handle processes the JSON of one "data:" line and returns the stream events
// it produced.
func (d *Decoder) Handle(data string) []harness.StreamEvent {
	data = strings.TrimSpace(data)
	if data == "" || d.ended {
		return nil
	}
	if data == "[DONE]" {
		return d.finish("FINISHED")
	}
	var c chunk
	if json.Unmarshal([]byte(data), &c) != nil {
		return nil
	}
	if msg := errorText(c); msg != "" {
		d.ended = true
		return []harness.StreamEvent{{Kind: harness.StreamError, Err: errors.New("qwen error: " + msg)}}
	}
	var out []harness.StreamEvent
	if c.Usage != nil {
		if u, ok := c.Usage.usage(); ok {
			out = append(out, harness.StreamEvent{Kind: harness.StreamUsage, Usage: &u})
		}
	}
	if len(c.Choices) == 0 {
		return out // response.created, usage-only chunks, ...
	}
	dl := c.Choices[0].Delta
	phase := strings.ToLower(dl.Phase)

	var s string
	if len(dl.Content) > 0 {
		_ = json.Unmarshal(dl.Content, &s) // not a string (a structured summary): ignore
	}

	switch {
	case phase == "" || phase == "answer":
		if s != "" {
			out = append(out, harness.StreamEvent{Kind: harness.StreamText, Text: s})
		}
		if strings.EqualFold(dl.Status, "finished") {
			out = append(out, d.finish("FINISHED")...)
		}
	case strings.Contains(phase, "think") && !strings.Contains(phase, "summary"):
		if s != "" {
			out = append(out, harness.StreamEvent{Kind: harness.StreamThinking, Text: s})
		}
	}
	return out
}

func (d *Decoder) finish(status string) []harness.StreamEvent {
	if d.ended {
		return nil
	}
	d.ended = true
	return []harness.StreamEvent{{Kind: harness.StreamEnd, Text: status}}
}

// errorText returns the server's complaint if the chunk is an error, or "".
func errorText(c chunk) string {
	if len(c.Error) > 0 && string(c.Error) != "null" {
		return describe(c.Error)
	}
	if c.Success != nil && !*c.Success {
		if msg := describe(c.Data); msg != "" {
			return msg
		}
		return "the request failed"
	}
	return ""
}

// describe pulls a readable message out of an error value: a string, or an
// object with details / message / code fields.
func describe(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Code    any    `json:"code"`
		Details string `json:"details"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(raw, &o) == nil {
		text := o.Details
		if text == "" {
			text = o.Message
		}
		if text == "" {
			text = o.Msg
		}
		switch {
		case text != "" && o.Code != nil && fmt.Sprint(o.Code) != "":
			return fmt.Sprintf("%s (%v)", text, o.Code)
		case text != "":
			return text
		case o.Code != nil:
			return fmt.Sprint(o.Code)
		}
	}
	return strings.TrimSpace(string(raw))
}

// maxRaw bounds how much of a non-event body is kept to explain a failure.
const maxRaw = 8 << 10

// Decode reads a raw response body and streams harness events. The channel
// closes when the body ends or ctx is cancelled. This is what the bridge feeds.
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
		var raw strings.Builder // lines that are not events: a refused request is plain JSON

		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			switch {
			case line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:"):
			case strings.HasPrefix(line, "data:"):
				payload := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
				for _, e := range d.Handle(payload) {
					if !send(e) {
						return
					}
				}
			default:
				if raw.Len() < maxRaw {
					raw.WriteString(line)
					raw.WriteByte('\n')
				}
			}
			if d.ended {
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err := sc.Err(); err != nil && !d.ended {
			send(harness.StreamEvent{Kind: harness.StreamError, Err: err})
			return
		}
		if d.ended {
			return
		}
		if raw.Len() > 0 {
			var c chunk
			if json.Unmarshal([]byte(raw.String()), &c) == nil {
				if msg := errorText(c); msg != "" {
					send(harness.StreamEvent{Kind: harness.StreamError, Err: errors.New("qwen error: " + msg)})
					return
				}
			}
			send(harness.StreamEvent{Kind: harness.StreamError, Err: errors.New("qwen answered with something that is not a reply: " + preview(raw.String(), 200))})
			return
		}
		send(harness.StreamEvent{Kind: harness.StreamError, Err: errors.New("the stream ended before the reply was finished")})
	}()
	return ch
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}
