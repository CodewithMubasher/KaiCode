// Package openai talks to any OpenAI-compatible chat completions endpoint
// (OpenAI, DeepSeek's official API, OpenRouter, Together, Groq, Ollama,
// LM Studio, vLLM, ...).
//
// It is the native tool-calling path and has nothing in common with the
// DeepSeek web chat: no DSML, no tool block inside a prompt. The tools go in
// the request's "tools" field, the instructions in a real system message, the
// model's calls come back as structured tool_calls and the results go back as
// "tool" messages. The provider is stateless: the agent owns the conversation
// and sends all of it with every request.
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
	"kai/internal/tools"
)

const (
	idleTimeout   = 2 * time.Minute // no data from the stream
	headerTimeout = 2 * time.Minute // no response headers
)

// Config describes one endpoint.
type Config struct {
	Name    string       // shown in messages
	BaseURL string       // e.g. https://api.openai.com/v1
	APIKey  string       // may be empty for local servers
	Model   string       // required
	Client  *http.Client // optional, for tests
}

// Provider implements harness.NativeProvider.
type Provider struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	echoReas bool // the endpoint streams "reasoning_content", so it expects it back in tool loops
	nextCall int  // numbers calls the endpoint gave no id
}

var _ harness.NativeProvider = (*Provider)(nil)

// New builds a provider.
func New(cfg Config) *Provider {
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

// ---- request ---------------------------------------------------------------

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"` // a string, or null for an assistant message that only calls tools
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []wireCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function"` // name, description, parameters
}

func (p *Provider) wireMessages(in []harness.ChatMessage) []wireMessage {
	p.mu.Lock()
	echo := p.echoReas
	p.mu.Unlock()

	out := make([]wireMessage, 0, len(in))
	for _, m := range in {
		w := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) > 0 {
			w.Content = nil
		} else {
			w.Content = m.Content
		}
		if echo && m.Role == "assistant" {
			w.ReasoningContent = m.Reasoning
		}
		for _, c := range m.ToolCalls {
			wc := wireCall{ID: c.ID, Type: "function"}
			wc.Function.Name = c.Name
			// Strict endpoints refuse arguments that are not valid JSON, and a
			// model's broken call is exactly that; the error result tells it so.
			wc.Function.Arguments = c.Arguments
			if !json.Valid([]byte(c.Arguments)) {
				wc.Function.Arguments = "{}"
			}
			w.ToolCalls = append(w.ToolCalls, wc)
		}
		out = append(out, w)
	}
	return out
}

func wireTools(specs []tools.Spec) []wireTool {
	out := make([]wireTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, wireTool{Type: "function", Function: json.RawMessage(s.JSON())})
	}
	return out
}

// Chat implements harness.NativeProvider.
func (p *Provider) Chat(ctx context.Context, req harness.ChatRequest) (<-chan harness.StreamEvent, error) {
	if strings.TrimSpace(p.cfg.Model) == "" {
		return nil, fmt.Errorf("no model is set for %s: choose one with Switch model", p.cfg.Name)
	}
	endpointURL, err := endpoint(p.cfg.BaseURL, "/chat/completions")
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"model":    p.cfg.Model,
		"messages": p.wireMessages(req.Messages),
		"stream":   true,
		// Ask for the final usage chunk. It is what makes the context gauge
		// show the provider's real token count instead of an estimate. Servers
		// that do not know the option simply ignore it.
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(req.Tools) > 0 {
		payload["tools"] = wireTools(req.Tools)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithCancel(ctx)
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	setHeaders(httpReq, p.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, networkError(err, p.cfg.BaseURL)
	}
	if resp.StatusCode/100 != 2 {
		defer cancel()
		defer resp.Body.Close()
		return nil, apiError(resp, p.cfg.BaseURL)
	}

	ch := make(chan harness.StreamEvent, 32)
	go p.stream(ctx, reqCtx, cancel, resp, ch)
	return ch, nil
}

// ---- reply -----------------------------------------------------------------

// argText reads a tool call's arguments. They are a JSON string holding JSON,
// but some servers send the object itself; both end up as JSON text.
type argText string

func (a *argText) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = argText(s)
		return nil
	}
	if string(b) == "null" {
		*a = ""
		return nil
	}
	*a = argText(b)
	return nil
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string  `json:"name"`
					Arguments argText `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageWire `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// usageWire is the usage object an OpenAI-compatible endpoint returns. Some
// servers name the fields input_tokens / output_tokens instead.
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

// callAcc collects one tool call from its streamed fragments.
type callAcc struct {
	id, name string
	args     strings.Builder
	scan     argScanner
	last     harness.ToolProgress // last progress sent for this call
	sentProg bool
}

// callSet collects the calls of one reply. Fragments belong to a call by
// "index"; a server that leaves the index out is followed by id instead.
type callSet struct {
	list    []*callAcc
	byIndex map[int]*callAcc
}

func (s *callSet) pick(index *int, id string) *callAcc {
	add := func() *callAcc {
		a := &callAcc{}
		s.list = append(s.list, a)
		return a
	}
	if index != nil {
		if s.byIndex == nil {
			s.byIndex = map[int]*callAcc{}
		}
		a := s.byIndex[*index]
		// A server that reuses one index for several calls still sends a new id.
		if a != nil && id != "" && a.id != "" && a.id != id {
			a = nil
		}
		if a == nil {
			a = add()
			s.byIndex[*index] = a
		}
		return a
	}
	if n := len(s.list); n > 0 && (id == "" || s.list[n-1].id == id) {
		return s.list[n-1]
	}
	return add()
}

func (p *Provider) stream(ctx, reqCtx context.Context, cancel context.CancelFunc, resp *http.Response, ch chan<- harness.StreamEvent) {
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

	idle := time.AfterFunc(idleTimeout, cancel)
	defer idle.Stop()

	var calls callSet
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
					if reason != "" {
						p.mu.Lock()
						p.echoReas = true
						p.mu.Unlock()
					} else {
						reason = choice.Delta.Reasoning
					}
					if reason != "" && !send(harness.StreamEvent{Kind: harness.StreamThinking, Text: reason}) {
						return
					}
					if choice.Delta.Content != "" && !send(harness.StreamEvent{Kind: harness.StreamText, Text: choice.Delta.Content}) {
						return
					}
					for _, tc := range choice.Delta.ToolCalls {
						a := calls.pick(tc.Index, tc.ID)
						if a.id == "" {
							a.id = tc.ID
						}
						if a.name == "" {
							a.name = tc.Function.Name
						}
						frag := string(tc.Function.Arguments)
						a.args.WriteString(frag)
						if (a.name == "edit_file" || a.name == "create_file") && frag != "" {
							if pr, ok := a.scan.Feed(frag); ok && (!a.sentProg || pr != a.last) {
								a.last, a.sentProg = pr, true
								hp := pr
								if !send(harness.StreamEvent{Kind: harness.StreamToolProgress, Tool: &hp}) {
									return
								}
							}
						}
					}
					if choice.FinishReason != nil && *choice.FinishReason != "" {
						finish = *choice.FinishReason
					}
				}
				// The final chunk carries the endpoint's own token count. It
				// has no choices, so it is read after them.
				if c.Usage != nil {
					if u, ok := c.Usage.usage(); ok {
						u := u
						if !send(harness.StreamEvent{Kind: harness.StreamUsage, Usage: &u}) {
							return
						}
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
	case finish == "length" || finish == "content_filter":
		// The calls, if any, may be cut off: do not run them. The agent
		// reports "the reply ended early (status length)".
		send(harness.StreamEvent{Kind: harness.StreamEnd, Text: finish})
	default:
		for _, tc := range p.finishCalls(&calls) {
			tc := tc
			if !send(harness.StreamEvent{Kind: harness.StreamToolCall, ToolCall: &tc}) {
				return
			}
		}
		send(harness.StreamEvent{Kind: harness.StreamEnd, Text: "FINISHED"})
	}
}

// finishCalls turns the collected fragments into complete calls. A call the
// server gave no id gets one, since every result must name its call.
func (p *Provider) finishCalls(s *callSet) []harness.ToolCall {
	var out []harness.ToolCall
	for _, a := range s.list {
		if strings.TrimSpace(a.name) == "" {
			continue
		}
		id := a.id
		if id == "" {
			p.mu.Lock()
			p.nextCall++
			id = fmt.Sprintf("call_%d", p.nextCall)
			p.mu.Unlock()
		}
		out = append(out, harness.ToolCall{ID: id, Name: a.name, Arguments: a.args.String()})
	}
	return out
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
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		if l := strings.ToLower(msg); strings.Contains(l, "tool") || strings.Contains(l, "function") {
			hint = " (this model may not support tool calling: choose one that does)"
		}
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
