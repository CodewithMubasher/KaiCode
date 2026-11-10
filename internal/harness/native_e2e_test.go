package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kai/internal/harness"
	"kai/internal/harness/provider/openai"
	"kai/internal/tools"
)

// The whole native path, end to end: the real agent, the real OpenAI provider
// and a real tool, against a fake chat-completions server that answers with
// streamed tool_calls. No DSML anywhere.
func TestNativeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(tools.EditFile{Dir: dir})
	reg.Register(tools.ReadFile{})

	const content = "<h1>Hi</h1>\n<p>there</p>\n"
	args, _ := json.Marshal(map[string]any{"path": "index.html", "old_string": "", "new_string": content})

	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		bodies = append(bodies, m)
		n := len(bodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		send := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s); w.(http.Flusher).Flush() }
		if n == 1 {
			send(`{"choices":[{"delta":{"content":"Creating the page."}}]}`)
			send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"edit_file","arguments":""}}]}}]}`)
			a := string(args)
			for i := 0; i < len(a); i += 11 { // arrives in small pieces
				send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + strconv.Quote(a[i:min(i+11, len(a))]) + `}}]}}]}`)
			}
			send(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			send(`{"choices":[{"delta":{"content":"Done: index.html is ready."},"finish_reason":"stop"}]}`)
		}
		send("[DONE]")
	}))
	defer srv.Close()

	prov := openai.New(openai.Config{Name: "fake", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"})
	a := harness.New(harness.Config{
		Native:       prov,
		Tools:        reg,
		System:       "WEB SYSTEM (must not be used)",
		NativeSystem: "You are Kai.",
		ToolTimeout:  10 * time.Second,
	})

	var evs []harness.Event
	timeout := time.After(10 * time.Second)
	ch := a.Run(context.Background(), "make a page")
loop:
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				break loop
			}
			evs = append(evs, e)
		case <-timeout:
			t.Fatal("run did not finish")
		}
	}

	var shown strings.Builder
	var sawCall, sawResult, sawProgress, done bool
	for _, e := range evs {
		switch e.Kind {
		case harness.EvText:
			shown.WriteString(e.Text)
		case harness.EvToolCall:
			sawCall = e.Call.Name == "edit_file" && e.Call.Args["path"] == "index.html"
		case harness.EvToolResult:
			sawResult = !e.Result.IsError
		case harness.EvToolProgress:
			sawProgress = e.Progress.Path == "index.html" && e.Progress.Creating
		case harness.EvDone:
			done = true
		case harness.EvError:
			t.Fatalf("error: %v", e.Err)
		}
	}
	if !sawCall || !sawResult || !sawProgress || !done {
		t.Fatalf("call=%v result=%v progress=%v done=%v", sawCall, sawResult, sawProgress, done)
	}
	if shown.String() != "Creating the page.Done: index.html is ready." {
		t.Fatalf("text: %q", shown.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil || string(got) != content {
		t.Fatalf("file: %q, %v", got, err)
	}

	// What went over the wire.
	if len(bodies) != 2 {
		t.Fatalf("want 2 requests, got %d", len(bodies))
	}
	raw, _ := json.Marshal(bodies)
	for _, bad := range []string{"DSML", "kai_instructions", "WEB SYSTEM", "<tool_result>"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("%q leaked into a native request:\n%s", bad, raw)
		}
	}
	first := bodies[0]["messages"].([]any)
	if first[0].(map[string]any)["role"] != "system" || first[0].(map[string]any)["content"] != "You are Kai." {
		t.Fatalf("system message: %v", first[0])
	}
	if names := bodies[0]["tools"].([]any); len(names) != 2 {
		t.Fatalf("both tools must be sent as API tools: %v", names)
	}
	second := bodies[1]["messages"].([]any)
	if len(second) != 4 {
		t.Fatalf("second request: %v", second)
	}
	as := second[2].(map[string]any)
	tc := as["tool_calls"].([]any)[0].(map[string]any)
	if as["role"] != "assistant" || tc["id"] != "call_1" || tc["function"].(map[string]any)["name"] != "edit_file" {
		t.Fatalf("assistant message: %v", as)
	}
	var echoed map[string]any
	if err := json.Unmarshal([]byte(tc["function"].(map[string]any)["arguments"].(string)), &echoed); err != nil || echoed["path"] != "index.html" {
		t.Fatalf("arguments must be echoed back as the JSON the model sent: %v", err)
	}
	tm := second[3].(map[string]any)
	if tm["role"] != "tool" || tm["tool_call_id"] != "call_1" || tm["content"] == "" {
		t.Fatalf("tool message: %v", tm)
	}
}
