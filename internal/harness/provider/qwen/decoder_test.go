package qwen

import (
	"context"
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

func decodeString(t *testing.T, s string) []harness.StreamEvent {
	t.Helper()
	return collect(t, Decode(context.Background(), strings.NewReader(s)))
}

func TestRealUsageIsReported(t *testing.T) {
	evs := decodeString(t, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\",\"phase\":\"answer\",\"status\":\"typing\"}}],\"usage\":{\"prompt_tokens\":900,\"completion_tokens\":12,\"total_tokens\":912}}\n\ndata: [DONE]\n\n")
	var got *harness.Usage
	for _, e := range evs {
		if e.Kind == harness.StreamUsage && e.Usage != nil {
			u := *e.Usage
			got = &u
		}
	}
	if got == nil || got.Total() != 912 || got.PromptTokens != 900 {
		t.Fatalf("usage = %+v, want the provider's real count", got)
	}
}

func lastError(evs []harness.StreamEvent) error {
	for _, e := range evs {
		if e.Kind == harness.StreamError {
			return e.Err
		}
	}
	return nil
}

// The captured reply to a greeting.
func TestDecodeRealCapture(t *testing.T) {
	f, err := os.Open("testdata/hello.sse")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	evs := collect(t, Decode(context.Background(), f))
	want := "Hello Mubasher! How can I help you today? Whether it's about full-stack development, college studies, or finding resources for self-improvement, feel free to ask."
	if got := textOf(evs, harness.StreamText); got != want {
		t.Errorf("text = %q", got)
	}
	if err := lastError(evs); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	last := evs[len(evs)-1]
	if last.Kind != harness.StreamEnd || last.Text != "FINISHED" {
		t.Errorf("last event = %+v, want the end of the reply", last)
	}
	ends := 0
	for _, e := range evs {
		if e.Kind == harness.StreamEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Errorf("%d end events, want 1", ends)
	}
}

// Thinking finishes first; only the answer finishing ends the reply.
func TestThinkingPhaseDoesNotEndTheReply(t *testing.T) {
	evs := decodeString(t, strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Let me ","phase":"think","status":"typing"}}]}`,
		`data: {"choices":[{"delta":{"role":"assistant","content":"think.","phase":"think","status":"typing"}}]}`,
		`data: {"choices":[{"delta":{"role":"assistant","content":"","phase":"think","status":"finished"}}]}`,
		`data: {"choices":[{"delta":{"role":"assistant","content":"Done.","phase":"answer","status":"typing"}}]}`,
		`data: {"choices":[{"delta":{"role":"assistant","content":"","phase":"answer","status":"finished"}}]}`,
	}, "\n\n")+"\n\n")

	if got := textOf(evs, harness.StreamThinking); got != "Let me think." {
		t.Errorf("thinking = %q", got)
	}
	if got := textOf(evs, harness.StreamText); got != "Done." {
		t.Errorf("text = %q", got)
	}
	if err := lastError(evs); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOtherPhasesAreIgnored(t *testing.T) {
	evs := decodeString(t, strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":{"title":"x"},"phase":"thinking_summary","status":"typing"}}]}`,
		`data: {"choices":[{"delta":{"role":"function","content":"{}","phase":"web_search","status":"typing"}}]}`,
		`data: {"choices":[{"delta":{"role":"assistant","content":"Hi","phase":"answer","status":"typing"}}]}`,
		`data: {"choices":[{"delta":{"role":"assistant","content":"","phase":"answer","status":"finished"}}]}`,
	}, "\n\n")+"\n\n")
	if got := textOf(evs, harness.StreamText); got != "Hi" {
		t.Errorf("text = %q", got)
	}
	if got := textOf(evs, harness.StreamThinking); got != "" {
		t.Errorf("thinking = %q, want none", got)
	}
}

// A refused request is a plain JSON body, not events.
func TestRefusedRequestReportsTheServersWords(t *testing.T) {
	evs := decodeString(t, `{"success":false,"request_id":"r1","data":{"code":"RateLimited","details":"Too many requests, try again later"}}`)
	err := lastError(evs)
	if err == nil || !strings.Contains(err.Error(), "Too many requests") || !strings.Contains(err.Error(), "RateLimited") {
		t.Errorf("error = %v", err)
	}
}

func TestInBandError(t *testing.T) {
	evs := decodeString(t, "data: {\"error\":{\"code\":\"x\",\"details\":\"model is busy\"}}\n\n")
	if err := lastError(evs); err == nil || !strings.Contains(err.Error(), "model is busy") {
		t.Errorf("error = %v", err)
	}
}

func TestStreamCutShort(t *testing.T) {
	evs := decodeString(t, `data: {"choices":[{"delta":{"role":"assistant","content":"Hel","phase":"answer","status":"typing"}}]}`+"\n\n")
	if got := textOf(evs, harness.StreamText); got != "Hel" {
		t.Errorf("text = %q", got)
	}
	if err := lastError(evs); err == nil || !strings.Contains(err.Error(), "before the reply was finished") {
		t.Errorf("error = %v", err)
	}
}

func TestDoneMarkerEndsTheReply(t *testing.T) {
	evs := decodeString(t, `data: {"choices":[{"delta":{"content":"ok","phase":"answer","status":"typing"}}]}`+"\n\ndata: [DONE]\n\n")
	if err := lastError(evs); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if evs[len(evs)-1].Kind != harness.StreamEnd {
		t.Errorf("events = %+v", evs)
	}
}
