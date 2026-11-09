package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kai/internal/harness"
)

func sse(w http.ResponseWriter, parts ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, p := range parts {
		fmt.Fprintf(w, "data: %s\n\n", p)
		w.(http.Flusher).Flush()
	}
}

func collect(t *testing.T, ch <-chan harness.StreamEvent) (text, thinking string, kinds []harness.StreamKind, end string, err error) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			kinds = append(kinds, e.Kind)
			switch e.Kind {
			case harness.StreamText:
				text += e.Text
			case harness.StreamThinking:
				thinking += e.Text
			case harness.StreamEnd:
				end = e.Text
			case harness.StreamError:
				err = e.Err
			}
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

func TestStreamAndHistory(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		bodies = append(bodies, m)
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		n := len(bodies)
		mu.Unlock()
		sse(w,
			`{"choices":[{"delta":{"reasoning_content":"hmm "}}]}`,
			fmt.Sprintf(`{"choices":[{"delta":{"content":"reply %d "}}]}`, n),
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`)
	}))
	defer srv.Close()

	p := New(Config{Name: "test", BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Model: "m1"})

	ch, err := p.Send(context.Background(), harness.Turn{Prompt: "first", Session: "s1", NewChat: true})
	if err != nil {
		t.Fatal(err)
	}
	text, thinking, kinds, end, serr := collect(t, ch)
	if serr != nil || end != "FINISHED" || text != "reply 1 done" || thinking != "hmm " {
		t.Fatalf("turn 1: text=%q thinking=%q end=%q err=%v", text, thinking, end, serr)
	}
	if kinds[0] != harness.StreamSent || kinds[1] != harness.StreamReady {
		t.Fatalf("a reply must open with Sent then Ready, got %v", kinds)
	}
	if auth != "Bearer sk-test" || path != "/v1/chat/completions" {
		t.Fatalf("request: auth=%q path=%q", auth, path)
	}

	// Second message of the same chat: the provider resends the whole history.
	ch, err = p.Send(context.Background(), harness.Turn{Prompt: "second", Session: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
	msgs := bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want user, assistant, user; got %d messages: %v", len(msgs), msgs)
	}
	if got := msgs[1].(map[string]any); got["role"] != "assistant" || got["content"] != "reply 1 done" {
		t.Fatalf("assistant turn missing from history: %v", got)
	}

	// A new chat forgets everything.
	ch, _ = p.Send(context.Background(), harness.Turn{Prompt: "fresh", Session: "s2", NewChat: true})
	collect(t, ch)
	if n := len(bodies[2]["messages"].([]any)); n != 1 {
		t.Fatalf("new chat must start with one message, got %d", n)
	}
}

func TestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key"}}`)
	}))
	defer srv.Close()
	p := New(Config{Name: "t", BaseURL: srv.URL, APIKey: "bad", Model: "m"})
	_, err := p.Send(context.Background(), harness.Turn{Prompt: "x", Session: "s", NewChat: true})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("want a 401 error with the provider's message, got %v", err)
	}
	if strings.Contains(err.Error(), "bad") && strings.Contains(err.Error(), "Bearer") {
		t.Fatal("the key must never appear in errors")
	}
	if len(p.history) != 0 {
		t.Fatal("a rejected request must not enter the history")
	}
}

func TestNoModel(t *testing.T) {
	p := New(Config{Name: "t", BaseURL: "http://127.0.0.1:1"})
	if _, err := p.Send(context.Background(), harness.Turn{Prompt: "x"}); err == nil {
		t.Fatal("want an error when no model is set")
	}
}

func TestStoppedReplyKeepsPartialText(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"partial"}}]}`)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	p := New(Config{Name: "t", BaseURL: srv.URL, Model: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Send(ctx, harness.Turn{Prompt: "go", Session: "s", NewChat: true})
	if err != nil {
		t.Fatal(err)
	}
	for e := range ch {
		if e.Kind == harness.StreamText {
			cancel() // like pressing esc
			break
		}
	}
	for range ch { // drain until the provider closes the channel
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.history); n != 2 || p.history[1].Content != "partial" {
		t.Fatalf("history after a stopped reply: %+v", p.history)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"b-model"},{"id":"a-model"}]}`)
	}))
	defer srv.Close()
	ids, err := ListModels(context.Background(), srv.URL+"/v1/chat/completions", "")
	if err != nil || len(ids) != 2 || ids[0] != "a-model" {
		t.Fatalf("models: %v %v", ids, err)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.openai.com/v1/":                 "https://api.openai.com/v1",
		" http://localhost:11434/v1 ":                "http://localhost:11434/v1",
		"https://x.ai/v1/chat/completions":           "https://x.ai/v1",
	} {
		if got, err := NormalizeBaseURL(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "api.openai.com/v1", "ftp://x", "https://"} {
		if _, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
