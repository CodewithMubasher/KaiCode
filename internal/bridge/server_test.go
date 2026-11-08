package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kai/internal/harness"
	"kai/internal/harness/provider/deepseek"
)

const token = "secret"

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func do(t *testing.T, method, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Kai-Token", token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// poll plays the extension's long-poll and returns the command it receives.
func poll(t *testing.T, base string) Command {
	t.Helper()
	resp := do(t, "GET", base+"/v1/poll", "", nil)
	defer resp.Body.Close()
	var c Command
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Errorf("poll: %v", err)
	}
	return c
}

func sendEvent(t *testing.T, base string, ev event) {
	t.Helper()
	b, _ := json.Marshal(ev)
	do(t, "POST", base+"/v1/event", string(b), nil).Body.Close()
}

func waitConnected(t *testing.T, s *Server) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if s.Connected() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the extension never showed as connected")
}

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

func TestRoundTrip(t *testing.T) {
	s, ts := newTestServer(t)

	cmdCh := make(chan Command, 1)
	go func() { cmdCh <- poll(t, ts.URL) }()
	waitConnected(t, s)

	evCh, err := s.Send(context.Background(), harness.Turn{Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := <-cmdCh
	if cmd.Type != "send" || cmd.Prompt != "hello" || !cmd.NewChat || cmd.ID == "" {
		t.Fatalf("command = %+v", cmd)
	}

	// The extension relays the page's raw SSE in arbitrary pieces.
	raw := string(deepseek.BuildSSE("Hel", "lo"))
	sendEvent(t, ts.URL, event{ID: cmd.ID, Type: "chunk", Data: raw[:40]})
	sendEvent(t, ts.URL, event{ID: cmd.ID, Type: "chunk", Data: raw[40:]})
	sendEvent(t, ts.URL, event{ID: cmd.ID, Type: "end"})

	var text strings.Builder
	for _, e := range collect(t, evCh) {
		if e.Kind == harness.StreamError {
			t.Fatalf("unexpected error: %v", e.Err)
		}
		if e.Kind == harness.StreamText {
			text.WriteString(e.Text)
		}
	}
	if text.String() != "Hello" {
		t.Errorf("text = %q", text.String())
	}
}

func TestContinuingAChatDoesNotAskForANewOne(t *testing.T) {
	s, ts := newTestServer(t)
	cmdCh := make(chan Command, 1)
	go func() { cmdCh <- poll(t, ts.URL) }()
	waitConnected(t, s)

	parent := 2
	if _, err := s.Send(context.Background(), harness.Turn{Prompt: "more", ParentID: &parent}); err != nil {
		t.Fatal(err)
	}
	if cmd := <-cmdCh; cmd.NewChat {
		t.Errorf("command = %+v, want newChat=false", cmd)
	}
}

func TestChatLostReachesTheHarness(t *testing.T) {
	s, ts := newTestServer(t)
	cmdCh := make(chan Command, 1)
	go func() { cmdCh <- poll(t, ts.URL) }()
	waitConnected(t, s)

	evCh, err := s.Send(context.Background(), harness.Turn{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := <-cmdCh
	sendEvent(t, ts.URL, event{ID: cmd.ID, Type: "chat_lost"})

	evs := collect(t, evCh)
	last := evs[len(evs)-1]
	if last.Kind != harness.StreamError || !errors.Is(last.Err, harness.ErrChatLost) {
		t.Errorf("last event = %+v", last)
	}
}

func TestBrowserErrorReachesTheHarness(t *testing.T) {
	s, ts := newTestServer(t)
	cmdCh := make(chan Command, 1)
	go func() { cmdCh <- poll(t, ts.URL) }()
	waitConnected(t, s)

	evCh, _ := s.Send(context.Background(), harness.Turn{Prompt: "x"})
	cmd := <-cmdCh
	sendEvent(t, ts.URL, event{ID: cmd.ID, Type: "error", Message: "input box not found"})

	evs := collect(t, evCh)
	last := evs[len(evs)-1]
	if last.Kind != harness.StreamError || !strings.Contains(last.Err.Error(), "input box not found") {
		t.Errorf("last event = %+v", last)
	}
}

func TestCancelTellsTheExtension(t *testing.T) {
	s, ts := newTestServer(t)
	first := make(chan Command, 1)
	go func() { first <- poll(t, ts.URL) }()
	waitConnected(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	evCh, _ := s.Send(ctx, harness.Turn{Prompt: "x"})
	cmd := <-first
	cancel()

	cancelCmd := poll(t, ts.URL)
	if cancelCmd.Type != "cancel" || cancelCmd.ID != cmd.ID {
		t.Errorf("command = %+v, want cancel for %s", cancelCmd, cmd.ID)
	}
	collect(t, evCh) // the stream must close
}

func TestNotConnected(t *testing.T) {
	s, _ := newTestServer(t)
	if _, err := s.Send(context.Background(), harness.Turn{Prompt: "x"}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("err = %v", err)
	}
}

func TestGuard(t *testing.T) {
	_, ts := newTestServer(t)

	resp := do(t, "GET", ts.URL+"/v1/status", "", map[string]string{"X-Kai-Token": "wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, "GET", ts.URL+"/v1/status", "", map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("web origin: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, "GET", ts.URL+"/v1/status", "", map[string]string{"Origin": "chrome-extension://abcdef"})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("extension origin: status %d", resp.StatusCode)
	}
	var body bytes.Buffer
	body.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(body.String(), "connected") {
		t.Errorf("status body = %q", body.String())
	}
}

func TestLoadOrCreateTokenIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateToken(dir)
	if err != nil || len(a) < 32 {
		t.Fatalf("token = %q, err = %v", a, err)
	}
	b, _ := LoadOrCreateToken(dir)
	if a != b {
		t.Error("the token must not change between runs")
	}
}
