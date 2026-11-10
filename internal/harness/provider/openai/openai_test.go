package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kai/internal/harness"
	"kai/internal/tools"
)

func sse(w http.ResponseWriter, parts ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, p := range parts {
		fmt.Fprintf(w, "data: %s\n\n", p)
		w.(http.Flusher).Flush()
	}
}

// collect drains a reply. Tool calls and progress are returned by collectAll;
// most tests only need the text.
func collect(t *testing.T, ch <-chan harness.StreamEvent) (text, thinking string, kinds []harness.StreamKind, end string, err error) {
	t.Helper()
	r := collectAll(t, ch)
	return r.text, r.thinking, r.kinds, r.end, r.err
}

type reply struct {
	text, thinking string
	kinds          []harness.StreamKind
	end            string
	err            error
	calls          []harness.ToolCall
	progress       []harness.ToolProgress
}

func collectAll(t *testing.T, ch <-chan harness.StreamEvent) (r reply) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			r.kinds = append(r.kinds, e.Kind)
			switch e.Kind {
			case harness.StreamText:
				r.text += e.Text
			case harness.StreamThinking:
				r.thinking += e.Text
			case harness.StreamEnd:
				r.end = e.Text
			case harness.StreamError:
				r.err = e.Err
			case harness.StreamToolCall:
				r.calls = append(r.calls, *e.ToolCall)
			case harness.StreamToolProgress:
				r.progress = append(r.progress, *e.Tool)
			}
		case <-timeout:
			t.Fatal("stream did not finish")
		}
	}
}

func echoSpec() tools.Spec {
	return tools.Spec{Name: "echo", Description: "Echo text.", Params: []tools.Param{{Name: "text", Type: "string", Required: true}}}
}

// serve runs a test server that records every request body and answers with
// the given SSE payloads.
func serve(t *testing.T, bodies *[]map[string]any, parts ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["_auth"], m["_path"] = r.Header.Get("Authorization"), r.URL.Path
		mu.Lock()
		*bodies = append(*bodies, m)
		mu.Unlock()
		sse(w, parts...)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatSendsSystemToolsAndStreams(t *testing.T) {
	var bodies []map[string]any
	srv := serve(t, &bodies,
		`{"choices":[{"delta":{"reasoning_content":"hmm "}}]}`,
		`{"choices":[{"delta":{"content":"hello "}}]}`,
		`{"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`[DONE]`)

	p := New(Config{Name: "test", BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Model: "m1"})
	ch, err := p.Chat(context.Background(), harness.ChatRequest{
		Messages: []harness.ChatMessage{{Role: "system", Content: "be Kai"}, {Role: "user", Content: "hi"}},
		Tools:    []tools.Spec{echoSpec()},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := collectAll(t, ch)
	if r.err != nil || r.end != "FINISHED" || r.text != "hello world" || r.thinking != "hmm " || len(r.calls) != 0 {
		t.Fatalf("reply: %+v", r)
	}
	for _, k := range r.kinds {
		if k == harness.StreamSent || k == harness.StreamReady {
			t.Fatalf("the native path has no Sent/Ready events, got %v", r.kinds)
		}
	}

	b := bodies[0]
	if b["_auth"] != "Bearer sk-test" || b["_path"] != "/v1/chat/completions" || b["model"] != "m1" || b["stream"] != true {
		t.Fatalf("request: %v", b)
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "be Kai" {
		t.Fatalf("the system prompt must be a real system message: %v", msgs)
	}
	tl := b["tools"].([]any)
	fn := tl[0].(map[string]any)["function"].(map[string]any)
	if tl[0].(map[string]any)["type"] != "function" || fn["name"] != "echo" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Fatalf("tools must be sent as structured function definitions: %v", tl)
	}
	if raw, _ := json.Marshal(b); strings.Contains(string(raw), "DSML") {
		t.Fatal("DSML must never appear in a native request")
	}
}

func TestNoToolsFieldWithoutTools(t *testing.T) {
	var bodies []map[string]any
	srv := serve(t, &bodies, `{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`, `[DONE]`)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "hi"}}})
	collect(t, ch)
	if _, has := bodies[0]["tools"]; has {
		t.Fatal("an empty tool list must not be sent")
	}
}

func TestStreamedToolCalls(t *testing.T) {
	var bodies []map[string]any
	srv := serve(t, &bodies,
		`{"choices":[{"delta":{"content":"Checking."}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"echo","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"te"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"echo","arguments":"{\"text\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"xt\":\"one\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"two\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`[DONE]`)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "go"}}, Tools: []tools.Spec{echoSpec()}})
	r := collectAll(t, ch)

	if r.err != nil || r.end != "FINISHED" || r.text != "Checking." {
		t.Fatalf("reply: %+v", r)
	}
	if len(r.calls) != 2 ||
		r.calls[0] != (harness.ToolCall{ID: "call_a", Name: "echo", Arguments: `{"text":"one"}`}) ||
		r.calls[1] != (harness.ToolCall{ID: "call_b", Name: "echo", Arguments: `{"text":"two"}`}) {
		t.Fatalf("calls must be assembled in order from the fragments: %+v", r.calls)
	}
	// Calls arrive after the text and before the end of the reply.
	last := r.kinds[len(r.kinds)-1]
	if last != harness.StreamEnd || r.kinds[len(r.kinds)-2] != harness.StreamToolCall {
		t.Fatalf("event order: %v", r.kinds)
	}
}

// Some servers (Ollama, many proxies) send each call whole, with no index and
// sometimes no id.
func TestToolCallsWithoutIndexOrID(t *testing.T) {
	var bodies []map[string]any
	srv := serve(t, &bodies,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"echo","arguments":{"text":"a"}}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"x1","function":{"name":"echo","arguments":"{\"text\":\"b\"}"}}]},"finish_reason":"stop"}]}`,
		`[DONE]`)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "go"}}})
	r := collectAll(t, ch)
	if r.err != nil || len(r.calls) != 2 {
		t.Fatalf("reply: %+v", r)
	}
	if r.calls[0].ID == "" || r.calls[0].Arguments != `{"text":"a"}` {
		t.Fatalf("a call without an id needs one, and an object argument must be kept as JSON: %+v", r.calls[0])
	}
	if r.calls[1].ID != "x1" || r.calls[1].ID == r.calls[0].ID {
		t.Fatalf("ids must be kept and unique: %+v", r.calls)
	}
}

func TestCutOffReplyDoesNotRunCalls(t *testing.T) {
	var bodies []map[string]any
	srv := serve(t, &bodies,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"echo","arguments":"{\"text\":\"unfini"}}]},"finish_reason":"length"}]}`,
		`[DONE]`)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "go"}}})
	r := collectAll(t, ch)
	if r.end != "length" || len(r.calls) != 0 {
		t.Fatalf("a call cut off by the length limit must not be sent on: %+v", r)
	}
}

// DSML is DeepSeek-web markup. On this path it is just text.
func TestDSMLInTheReplyIsPlainText(t *testing.T) {
	const markup = `<|DSML|tool_calls><|DSML|invoke name="echo"></|DSML|invoke></|DSML|tool_calls>`
	var bodies []map[string]any
	srv := serve(t, &bodies,
		`{"choices":[{"delta":{"content":`+strconv.Quote(markup)+`},"finish_reason":"stop"}]}`, `[DONE]`)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "go"}}})
	r := collectAll(t, ch)
	if r.text != markup || len(r.calls) != 0 {
		t.Fatalf("text must pass through untouched and make no calls: %+v", r)
	}
}

func TestConversationOnTheWire(t *testing.T) {
	var bodies []map[string]any
	srv := serve(t, &bodies,
		`{"choices":[{"delta":{"reasoning_content":"r"}}]}`,
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	conv := []harness.ChatMessage{
		{Role: "user", Content: "go"},
		{Role: "assistant", Reasoning: "thinking", ToolCalls: []harness.ToolCall{
			{ID: "c1", Name: "echo", Arguments: `{"text":"a"}`},
			{ID: "c2", Name: "echo", Arguments: `{broken`},
		}},
		{Role: "tool", ToolCallID: "c1", Content: "echo:a"},
		{Role: "tool", ToolCallID: "c2", Content: "Error: bad"},
	}

	// Before the endpoint ever streams reasoning_content, none is sent back.
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: conv})
	collect(t, ch)
	msgs := bodies[0]["messages"].([]any)
	as := msgs[1].(map[string]any)
	if v, has := as["content"]; !has || v != nil {
		t.Fatalf("an assistant message that only calls tools has null content: %v", as)
	}
	if _, has := as["reasoning_content"]; has {
		t.Fatal("reasoning is sent back only to endpoints that stream it")
	}
	calls := as["tool_calls"].([]any)
	c1 := calls[0].(map[string]any)
	if c1["id"] != "c1" || c1["type"] != "function" || c1["function"].(map[string]any)["arguments"] != `{"text":"a"}` {
		t.Fatalf("tool_calls: %v", calls)
	}
	if got := calls[1].(map[string]any)["function"].(map[string]any)["arguments"]; got != "{}" {
		t.Fatalf("arguments that are not JSON must not be echoed to a strict API, got %v", got)
	}
	tm := msgs[2].(map[string]any)
	if tm["role"] != "tool" || tm["tool_call_id"] != "c1" || tm["content"] != "echo:a" {
		t.Fatalf("tool message: %v", tm)
	}

	// The first reply streamed reasoning_content: now the endpoint gets it back.
	ch, _ = p.Chat(context.Background(), harness.ChatRequest{Messages: conv})
	collect(t, ch)
	if got := bodies[1]["messages"].([]any)[1].(map[string]any)["reasoning_content"]; got != "thinking" {
		t.Fatalf("reasoning_content must be echoed in a tool loop, got %v", got)
	}
}

func TestEditFileProgressWhileStreaming(t *testing.T) {
	frags := []string{`{"path":"a.html",`, `"old_string":"","new_string":"<p>\n`, `<p>\n<p>\n`, `<p>"}`}
	parts := []string{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"edit_file","arguments":""}}]}}]}`}
	for _, f := range frags {
		parts = append(parts, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":`+strconv.Quote(f)+`}}]}}]}`)
	}
	parts = append(parts, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	var bodies []map[string]any
	srv := serve(t, &bodies, parts...)
	p := New(Config{BaseURL: srv.URL, Model: "m"})
	ch, _ := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "go"}}})
	r := collectAll(t, ch)

	if len(r.progress) == 0 {
		t.Fatal("no progress reported")
	}
	first, last := r.progress[0], r.progress[len(r.progress)-1]
	if first.Path != "a.html" || first.Name != "edit_file" || !first.Creating {
		t.Fatalf("first progress: %+v", first)
	}
	if last.Lines != 4 {
		t.Fatalf("want 4 lines at the end, got %+v", last)
	}
	for i := 1; i < len(r.progress); i++ {
		if r.progress[i] == r.progress[i-1] {
			t.Fatalf("an unchanged progress must not be repeated: %+v", r.progress)
		}
	}
	if len(r.calls) != 1 || r.calls[0].Name != "edit_file" {
		t.Fatalf("calls: %+v", r.calls)
	}
}

func TestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key"}}`)
	}))
	defer srv.Close()
	p := New(Config{Name: "t", BaseURL: srv.URL, APIKey: "bad", Model: "m"})
	_, err := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("want a 401 error with the provider's message, got %v", err)
	}
	if strings.Contains(err.Error(), "bad") && strings.Contains(err.Error(), "Bearer") {
		t.Fatal("the key must never appear in errors")
	}
}

func TestModelWithoutToolSupportIsExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"registry.ollama.ai/library/gemma does not support tools"}}`)
	}))
	defer srv.Close()
	p := New(Config{Name: "t", BaseURL: srv.URL, Model: "gemma"})
	_, err := p.Chat(context.Background(), harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "x"}}, Tools: []tools.Spec{echoSpec()}})
	if err == nil || !strings.Contains(err.Error(), "tool calling") {
		t.Fatalf("the error should say the model may lack tool calling, got %v", err)
	}
}

func TestNoModel(t *testing.T) {
	p := New(Config{Name: "t", BaseURL: "http://127.0.0.1:1"})
	if _, err := p.Chat(context.Background(), harness.ChatRequest{}); err == nil {
		t.Fatal("want an error when no model is set")
	}
}

func TestStoppedReplyClosesTheStream(t *testing.T) {
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
	defer cancel()
	ch, err := p.Chat(ctx, harness.ChatRequest{Messages: []harness.ChatMessage{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	for e := range ch {
		if e.Kind == harness.StreamText {
			cancel() // like pressing esc
			break
		}
	}
	done := make(chan struct{})
	go func() {
		for range ch { // the provider must close the channel
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not close after the request was cancelled")
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
		"https://api.openai.com/v1/":       "https://api.openai.com/v1",
		" http://localhost:11434/v1 ":      "http://localhost:11434/v1",
		"https://x.ai/v1/chat/completions": "https://x.ai/v1",
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
