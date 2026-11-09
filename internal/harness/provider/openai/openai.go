// Package openai talks to any OpenAI-compatible chat completions endpoint
// (OpenAI, DeepSeek's official API, OpenRouter, Together, Groq, Ollama,
// LM Studio, vLLM, ...). Unlike a web chat, these APIs are stateless, so the
// provider keeps the conversation itself and sends it with every request.
//
// Tool calls still use Kai's DSML text format, which works with any model that
// follows instructions; native tool_calls can come later.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"kai/internal/harness"
)

const (
	idleTimeout     = 2 * time.Minute  // no data from the stream
	headerTimeout   = 2 * time.Minute  // no response headers
	defaultMaxBytes = 600 << 10        // history kept per chat (about 150k tokens)
)

// Config describes one endpoint.
type Config struct {
	Name            string       // shown in messages
	BaseURL         string       // e.g. https://api.openai.com/v1
	APIKey          string       // may be empty for local servers
	Model           string       // required
	Client          *http.Client // optional, for tests
	MaxHistoryBytes int          // 0 = default
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Provider implements harness.Provider.
type Provider struct {
	cfg    Config
	client *http.Client

	mu      sync.Mutex
	session string
	history []message
	nextID  int
}

// New builds a provider.
func New(cfg Config) *Provider {
	if cfg.MaxHistoryBytes <= 0 {
		cfg.MaxHistoryBytes = defaultMaxBytes
	}
	c := cfg.Client
	if c == nil {
		c = newClient()
	}
	return &Provider{cfg: cfg, client: c}
}

func newClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: tr}
}

// NormalizeBaseURL checks a base URL and tidies it: surrounding spaces, a
// trailing slash and a pasted "/chat/completions" are removed.
func NormalizeBaseURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("Base URL must start with http:// or https:// (for example https://api.openai.com/v1)")
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	return strings.TrimRight(base, "/"), nil
}

func endpoint(base, path string) (string, error) {
	b, err := NormalizeBaseURL(base)
	if err != nil {
		return "", err
	}
	return b + path, nil
}

func hostOf(base string) string {
	if u, err := url.Parse(strings.TrimSpace(base)); err == nil && u.Host != "" {
		return u.Host
	}
	return "the provider"
}

// Send implements harness.Provider. The agent sends only new content after
// the first message of a chat, so the full conversation is rebuilt here.
func (p *Provider) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
	if strings.TrimSpace(p.cfg.Model) == "" {
		return nil, fmt.Errorf("no model is set for %s: choose one with Switch model", p.cfg.Name)
	}
	endpointURL, err := endpoint(p.cfg.BaseURL, "/chat/completions")
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if t.NewChat || t.Session != p.session {
		p.history, p.session = nil, t.Session
	}
	msgs := append(append([]message(nil), p.history...), message{Role: "user", Content: t.Prompt})
	msgs = trim(msgs, p.cfg.MaxHistoryBytes)
	p.mu.Unlock()

	body, err := json.Marshal(map[string]any{"model": p.cfg.Model, "messages": msgs, "stream": true})
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	setHeaders(req, p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(req)
	if err != nil {
		cancel()
		return nil, networkError(err, p.cfg.BaseURL)
	}
	if resp.StatusCode/100 != 2 {
		defer cancel()
		defer resp.Body.Close()
		return nil, apiError(resp, p.cfg.BaseURL)
	}

	// The provider accepted the request: from now on the chat holds this
	// prompt, whatever happens to the reply.
	p.mu.Lock()
	p.history = msgs
	p.nextID += 2
	id := p.nextID
	p.mu.Unlock()

	ch := make(chan harness.StreamEvent, 32)
	go p.stream(ctx, reqCtx, cancel, resp, id, ch)
	return ch, nil
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *Provider) stream(ctx, reqCtx context.Context, cancel context.CancelFunc, resp *http.Response, id int, ch chan<- harness.StreamEvent) {
	defer close(ch) // runs last: the agent may send again as soon as it closes
	defer cancel()
	defer resp.Body.Close()

	send := func(e harness.StreamEvent) bool {
		select {
		case ch <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var reply strings.Builder
	// Keep the provider's record equal to what the user saw, including a
	// reply that was stopped half way.
	defer func() {
		if s := reply.String(); s != "" {
			p.mu.Lock()
			p.history = append(p.history, message{Role: "assistant", Content: s})
			p.mu.Unlock()
		}
	}()

	if !send(harness.StreamEvent{Kind: harness.StreamSent}) ||
		!send(harness.StreamEvent{Kind: harness.StreamReady, MessageID: id}) {
		return
	}

	idle := time.AfterFunc(idleTimeout, cancel)
	defer idle.Stop()

	r := bufio.NewReaderSize(resp.Body, 64<<10)
	finish, done := "", false
	var rerr error
	for !done {
		var line string
		line, rerr = r.ReadString('\n')
		idle.Reset(idleTimeout)

		if data, ok := sseData(line); ok {
			if data == "[DONE]" {
				done = true
				break
			}
			var c chunk
			if json.Unmarshal([]byte(data), &c) == nil {
				if c.Error != nil && c.Error.Message != "" {
					send(harness.StreamEvent{Kind: harness.StreamError, Err: fmt.Errorf("%s: %s", hostOf(p.cfg.BaseURL), c.Error.Message)})
					return
				}
				for _, choice := range c.Choices {
					reason := choice.Delta.ReasoningContent
					if reason == "" {
						reason = choice.Delta.Reasoning
					}
					if reason != "" && !send(harness.StreamEvent{Kind: harness.StreamThinking, Text: reason}) {
						return
					}
					if choice.Delta.Content != "" {
						reply.WriteString(choice.Delta.Content)
						if !send(harness.StreamEvent{Kind: harness.StreamText, Text: choice.Delta.Content}) {
							return
						}
					}
					if choice.FinishReason != nil && *choice.FinishReason != "" {
						finish = *choice.FinishReason
					}
				}
			}
		}
		if rerr != nil {
			break
		}
	}

	switch {
	case ctx.Err() != nil:
		return // stopped by the user
	case reqCtx.Err() != nil:
		send(harness.StreamEvent{Kind: harness.StreamError, Err: errors.New("the reply stalled: no data for two minutes")})
	case !done && finish == "":
		err := errors.New("the stream closed before the reply was finished")
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			err = fmt.Errorf("the connection to %s broke: %v", hostOf(p.cfg.BaseURL), rerr)
		}
		send(harness.StreamEvent{Kind: harness.StreamError, Err: err})
	default:
		status := "FINISHED"
		if finish == "length" || finish == "content_filter" {
			status = finish // the agent reports "the reply ended early (status length)"
		}
		send(harness.StreamEvent{Kind: harness.StreamEnd, Text: status})
	}
}

// sseData returns the payload of a "data:" line.
func sseData(line string) (string, bool) {
	line = strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// trim drops the oldest messages after the first one until the history fits
// the budget. The newest message is always kept.
func trim(msgs []message, budget int) []message {
	size := 0
	for _, m := range msgs {
		size += len(m.Content)
	}
	if size <= budget || len(msgs) <= 2 {
		return msgs
	}
	out := append([]message(nil), msgs...)
	for size > budget && len(out) > 2 {
		size -= len(out[1].Content)
		out = append(out[:1], out[2:]...)
	}
	return out
}

func setHeaders(req *http.Request, key string) {
	if k := strings.TrimSpace(key); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	req.Header.Set("User-Agent", "kai-code")
}

func networkError(err error, base string) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s did not answer in time", hostOf(base))
	}
	return fmt.Errorf("could not reach %s: %v", hostOf(base), err)
}

func apiError(resp *http.Response, base string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.Error.Message != "" {
			msg = e.Error.Message
		} else if e.Message != "" {
			msg = e.Message
		}
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	hint := ""
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = " (check the API key)"
	case http.StatusNotFound:
		hint = " (check the Base URL and model name; most providers need a /v1 suffix)"
	case http.StatusTooManyRequests:
		hint = " (rate limited or out of quota)"
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s answered %d%s: %s", hostOf(base), resp.StatusCode, hint, msg)
}

// ListModels asks the endpoint which models it offers (GET {base}/models).
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	u, err := endpoint(baseURL, "/models")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req, apiKey)
	resp, err := newClient().Do(req)
	if err != nil {
		return nil, networkError(err, baseURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, apiError(resp, baseURL)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s returned an unreadable model list", hostOf(baseURL))
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s listed no models", hostOf(baseURL))
	}
	sort.Strings(ids)
	return ids, nil
}
