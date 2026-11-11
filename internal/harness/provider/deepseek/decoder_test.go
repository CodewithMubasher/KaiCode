package deepseek

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"kai/internal/harness"
)

func collect(t *testing.T, ch <-chan harness.StreamEvent) []harness.StreamEvent {
	t.Helper()
	var out []harness.StreamEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

func textOf(evs []harness.StreamEvent, kind harness.StreamKind) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == kind {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

// The captured reply to "Hi there".
func TestDecodeRealCapture(t *testing.T) {
	f, err := os.Open("testdata/hello.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	evs := collect(t, Decode(context.Background(), f))

	if got := textOf(evs, harness.StreamText); got != "Hello! How can I help you today?" {
		t.Errorf("text = %q", got)
	}
	if got := textOf(evs, harness.StreamTitle); got != "Hello greeting" {
		t.Errorf("title = %q", got)
	}
	if evs[0].Kind != harness.StreamReady || evs[0].MessageID != 2 {
		t.Errorf("first event = %+v, want ready with id 2", evs[0])
	}
	ends := 0
	for _, e := range evs {
		if e.Kind == harness.StreamEnd {
			ends++
			if e.Text != "FINISHED" {
				t.Errorf("end status = %q", e.Text)
			}
		}
		if e.Kind == harness.StreamError {
			t.Errorf("unexpected error: %v", e.Err)
		}
	}
	if ends != 1 {
		t.Errorf("got %d end events, want 1", ends)
	}
}

func TestBuildSSERoundTrip(t *testing.T) {
	body := BuildSSE("line one\n", "with \"quotes\" and <|DSML|", "tool_calls>", " end")
	evs := collect(t, Decode(context.Background(), strings.NewReader(string(body))))
	want := "line one\nwith \"quotes\" and <|DSML|tool_calls> end"
	if got := textOf(evs, harness.StreamText); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// The shape of reasoning fragments is an assumption (a THINK fragment first,
// then a RESPONSE fragment appended). Replace with a real capture taken with
// thinking_enabled=true.
func TestDecodeThinkingFragments(t *testing.T) {
	body := "data: {\"v\":{\"response\":{\"status\":\"WIP\",\"fragments\":[{\"id\":1,\"type\":\"THINK\",\"content\":\"hm\"}]}}}\n\n" +
		"data: {\"p\":\"response/fragments/-1/content\",\"o\":\"APPEND\",\"v\":\"mm\"}\n\n" +
		"data: {\"p\":\"response/fragments\",\"o\":\"APPEND\",\"v\":[{\"id\":2,\"type\":\"RESPONSE\",\"content\":\"A\"}]}\n\n" +
		"data: {\"v\":\"B\"}\n\n" +
		"data: {\"p\":\"response/status\",\"o\":\"SET\",\"v\":\"FINISHED\"}\n\n"
	evs := collect(t, Decode(context.Background(), strings.NewReader(body)))
	if got := textOf(evs, harness.StreamThinking); got != "hmmm" {
		t.Errorf("thinking = %q", got)
	}
	if got := textOf(evs, harness.StreamText); got != "AB" {
		t.Errorf("text = %q", got)
	}
}

func TestDecodeStreamCutShort(t *testing.T) {
	body := BuildSSE("partial")
	cut := body[:strings.Index(string(body), "data: {\"p\":\"response/status\"")]
	evs := collect(t, Decode(context.Background(), strings.NewReader(string(cut))))
	last := evs[len(evs)-1]
	if last.Kind != harness.StreamError || last.Err == nil {
		t.Errorf("last event = %+v, want an error", last)
	}
}

// A follow-up turn in an existing chat (search_enabled, no title event).
// The real capture reports its running total as accumulated_token_usage.
func TestDecodeReportsRealUsage(t *testing.T) {
	f, err := os.Open("testdata/continue.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	evs := collect(t, Decode(context.Background(), f))
	var got *harness.Usage
	for _, e := range evs {
		if e.Kind == harness.StreamUsage && e.Usage != nil {
			u := *e.Usage
			got = &u
		}
	}
	if got == nil || got.Total() != 90 {
		t.Fatalf("usage = %+v, want the capture's real total of 90", got)
	}
}

// A single counter leaf is not the total: only the accumulated field is.
func TestUsageLeafIsNotMistakenForTotal(t *testing.T) {
	d := NewDecoder()
	evs := d.apply("response/usage/prompt_tokens", "SET", json.RawMessage("500"))
	if len(evs) != 0 {
		t.Fatalf("a per-field counter must not become a total: %+v", evs)
	}
	evs = d.apply("response/usage", "SET", json.RawMessage(`{"prompt_tokens":500,"completion_tokens":20}`))
	if len(evs) != 1 || evs[0].Kind != harness.StreamUsage || evs[0].Usage.Total() != 520 {
		t.Fatalf("usage object = %+v", evs)
	}
}

func TestDecodeSecondCapture(t *testing.T) {
	f, err := os.Open("testdata/continue.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	evs := collect(t, Decode(context.Background(), f))

	want := "Hi there! What's on your mind? I'm happy to help with a question, a task, or just a chat."
	if got := textOf(evs, harness.StreamText); got != want {
		t.Errorf("text = %q", got)
	}
	if evs[0].Kind != harness.StreamReady || evs[0].MessageID != 4 {
		t.Errorf("first event = %+v, want ready with id 4", evs[0])
	}
	if last := evs[len(evs)-1]; last.Kind != harness.StreamEnd || last.Text != "FINISHED" {
		t.Errorf("last event = %+v", last)
	}
}
