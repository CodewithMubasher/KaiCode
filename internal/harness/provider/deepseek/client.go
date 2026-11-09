package deepseek

import (
"bufio"
"bytes"
"context"
"encoding/json"
"fmt"
"io"
"net/http"
"os"
"strings"
"sync"
"time"

"kai/internal/harness"
)

type Auth struct {
Token          string `json:"token"`
Cookie         string `json:"cookie"`
DeviceID       string `json:"device_id"`
UserAgent      string `json:"user_agent"`
ClientVersion  string `json:"client_version"`
Locale         string `json:"locale"`
TimezoneOffset int    `json:"timezone_offset"`
}

type Client struct {
auth     Auth
http     *http.Client
pow      *Pow
mu       sync.Mutex
baseURL  string
authPath string
}

func NewClient(authPath, wasmPath string) (*Client, error) {
pow, err := NewPow(wasmPath)
if err != nil {
return nil, err
}
return &Client{
http:     &http.Client{Timeout: 180 * time.Second},
pow:      pow,
baseURL:  "https://chat.deepseek.com",
authPath: authPath,
}, nil
}

func (c *Client) loadAuth() error {
c.mu.Lock()
defer c.mu.Unlock()
b, err := os.ReadFile(c.authPath)
if err != nil {
return fmt.Errorf("missing %s: start Kai and load the extension", c.authPath)
}
return json.Unmarshal(b, &c.auth)
}

func (c *Client) headers(auth bool) map[string]string {
h := map[string]string{
"accept":                    "*/*",
"accept-language":           "en-US,en;q=0.9",
"origin":                    c.baseURL,
"referer":                   c.baseURL + "/",
"user-agent":                c.auth.UserAgent,
"x-client-bundle-id":        "com.deepseek.chat",
"x-client-locale":           c.auth.Locale,
"x-client-platform":         "web",
"x-client-timezone-offset":  fmt.Sprintf("%d", c.auth.TimezoneOffset),
"x-client-version":          c.auth.ClientVersion,
"x-device-id":               c.auth.DeviceID,
"content-type":              "application/json",
}
if auth {
h["authorization"] = "Bearer " + c.auth.Token
h["cookie"] = c.auth.Cookie
}
return h
}

func (c *Client) post(path string, body any) (json.RawMessage, error) {
b, _ := json.Marshal(body)
req, _ := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(b))
for k, v := range c.headers(true) {
req.Header.Set(k, v)
}
resp, err := c.http.Do(req)
if err != nil {
return nil, err
}
defer resp.Body.Close()
if resp.StatusCode == 401 {
return nil, fmt.Errorf("401 Unauthorized: token expired, reload DeepSeek in Chrome")
}
if resp.StatusCode != 200 {
return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
}
raw, _ := io.ReadAll(resp.Body)
var j struct {
Code int `json:"code"`
Data struct {
BizCode int             `json:"biz_code"`
BizData json.RawMessage `json:"biz_data"`
BizMsg  string          `json:"biz_msg"`
} `json:"data"`
Msg string `json:"msg"`
}
if err := json.Unmarshal(raw, &j); err != nil {
return nil, err
}
if j.Code != 0 || j.Data.BizCode != 0 {
return nil, fmt.Errorf("api error: %s %s", j.Msg, j.Data.BizMsg)
}
return j.Data.BizData, nil
}

func (c *Client) CreateSession() (string, error) {
data, err := c.post("/api/v0/chat_session/create", map[string]any{})
if err != nil {
return "", err
}
var res struct {
ChatSession struct {
ID string `json:"id"`
} `json:"chat_session"`
}
json.Unmarshal(data, &res)
return res.ChatSession.ID, nil
}

func (c *Client) powHeader() (string, error) {
data, err := c.post("/api/v0/chat/create_pow_challenge", map[string]any{"target_path": "/api/v0/chat/completion"})
if err != nil {
return "", err
}
var res struct {
Challenge struct {
Algorithm   string  `json:"algorithm"`
Challenge   string  `json:"challenge"`
Salt        string  `json:"salt"`
ExpireAt    int64   `json:"expire_at"`
Difficulty  int     `json:"difficulty"`
Signature   string  `json:"signature"`
TargetPath  string  `json:"target_path"`
} `json:"challenge"`
}
json.Unmarshal(data, &res)

chMap := map[string]interface{}{
"algorithm":   res.Challenge.Algorithm,
"challenge":   res.Challenge.Challenge,
"salt":        res.Challenge.Salt,
"signature":   res.Challenge.Signature,
"target_path": res.Challenge.TargetPath,
}

ans, err := c.pow.SolveChallenge(res.Challenge.Challenge, res.Challenge.Salt, res.Challenge.ExpireAt, res.Challenge.Difficulty)
if err != nil {
return "", err
}
return PowHeader(chMap, ans), nil
}

func (c *Client) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
if err := c.loadAuth(); err != nil {
return nil, err
}

sessionID := t.Session
if sessionID == "" {
var err error
sessionID, err = c.CreateSession()
if err != nil {
return nil, err
}
}

powH, err := c.powHeader()
if err != nil {
return nil, err
}

body := map[string]any{
"chat_session_id":    sessionID,
"parent_message_id":  t.ParentID,
"model_type":         map[bool]string{true: "default", false: ""}[t.ParentID == nil],
"prompt":             t.Prompt,
"ref_file_ids":       []string{},
"thinking_enabled":   t.Thinking,
"search_enabled":     false,
"action":             nil,
"preempt":            false,
}
b, _ := json.Marshal(body)
req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v0/chat/completion", bytes.NewReader(b))
for k, v := range c.headers(true) {
req.Header.Set(k, v)
}
req.Header.Set("x-ds-pow-response", powH)

resp, err := c.http.Do(req)
if err != nil {
return nil, err
}
if resp.StatusCode != 200 {
resp.Body.Close()
return nil, fmt.Errorf("completion failed: HTTP %d", resp.StatusCode)
}

out := make(chan harness.StreamEvent, 64)
go func() {
defer close(out)
defer resp.Body.Close()
sc := bufio.NewScanner(resp.Body)
sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

var event string
d := NewDecoder() // From your existing decoder.go
for sc.Scan() {
line := sc.Text()
if strings.HasPrefix(line, "event:") {
event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
} else if strings.HasPrefix(line, "data:") {
data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
if data == "" { continue }

for _, ev := range d.Handle(event, data) {
select {
case out <- ev:
case <-ctx.Done():
return
}
}
}
}
}()
return out, nil
}
