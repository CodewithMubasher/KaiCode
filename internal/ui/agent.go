package ui

import (
"context"
"encoding/json"
"fmt"
"log/slog"
"os"
"path/filepath"
"regexp"
"strconv"
"strings"
"sync"
"time"

tea "charm.land/bubbletea/v2"

"kai/internal/bridge"
"kai/internal/config"
"kai/internal/harness"
"kai/internal/tools"
"kai/internal/ui/components"
)

func kaiSystem() string {
return "You are Kai, a coding agent working in the user's project directory.\n\n" + tools.SystemGuide()
}

const toolTimeout = 11 * time.Minute

type runState struct {
agent   *harness.Agent
events  <-chan harness.Event
cancel  context.CancelFunc
id      int
thinkAt time.Time
tokens  int
}

type (
agentMsg struct {
id int
ev harness.Event
}
agentDoneMsg struct{ id int }
)

func newRunState(cfg *config.Config, ps *persister, hub *providerHub) runState {
reg := tools.NewRegistry()
reg.Register(tools.ReadFile{})
reg.Register(tools.Shell{})
reg.Register(tools.EditFile{})

active := activeProvider{Kind: "web"}
switch {
case cfg.Provider.Name == "mock":
active = activeProvider{Kind: "mock"}
case hub.st != nil:
if v, ok := hub.st.Setting(settingActive); ok {
active = parseActive(v)
}
}
prov, label, err := hub.build(active)
if err != nil {
slog.Warn("the saved provider is unavailable, using DeepSeek Web", "provider", active.String(), "err", err)
active = activeProvider{Kind: "web"}
prov, label, _ = hub.build(active)
}
hub.active, hub.label = active, label
ps.setProvider(label)

record := ps.record
if hub.st == nil {
record = sessionLogger(cfg.Session.Dir)
}

return runState{agent: harness.New(harness.Config{
Provider:    prov,
Tools:       reg,
System:      kaiSystem(),
ToolTimeout: toolTimeout,
Record:      record,
})}
}

func startBridge(cfg *config.Config) harness.Provider {
addr := cfg.Provider.Addr
if addr == "" {
addr = "127.0.0.1:8765"
}
srv, err := bridge.Start(addr, slog.Default())
if err != nil {
slog.Error("could not start the bridge", "addr", addr, "err", err)
return brokenProvider{fmt.Errorf("could not start the bridge on %s (is another Kai running?): %w", addr, err)}
}
return srv
}

type brokenProvider struct{ err error }

func (b brokenProvider) Send(ctx context.Context, t harness.Turn) (<-chan harness.StreamEvent, error) {
return nil, b.err
}

func sessionLogger(dir string) func(harness.Entry) {
if dir == "" {
return nil
}
var mu sync.Mutex
var f *os.File
return func(e harness.Entry) {
mu.Lock()
defer mu.Unlock()
if f == nil {
if os.MkdirAll(dir, 0o755) != nil {
return
}
name := filepath.Join(dir, time.Now().Format("20060102-150405")+".jsonl")
file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
if err != nil {
return
}
f = file
}
if b, err := json.Marshal(e); err == nil {
_, _ = f.Write(append(b, '\n'))
}
}
}

func (m Model) startRun(text string) (Model, tea.Cmd) {
ctx, cancel := context.WithCancel(context.Background())
m.run.id++
m.run.cancel = cancel
m.run.events = m.agent().Run(ctx, text)
m.run.thinkAt = time.Time{}
return m, waitEvent(m.run.id, m.run.events)
}

func (m Model) agent() *harness.Agent { return m.run.agent }

func (m Model) stopRun() Model {
if m.run.cancel != nil {
m.run.cancel()
m.run.cancel = nil
}
m.run.id++
m.run.events = nil
return m
}

func waitEvent(id int, ch <-chan harness.Event) tea.Cmd {
return func() tea.Msg {
ev, ok := <-ch
if !ok {
return agentDoneMsg{id}
}
return agentMsg{id, ev}
}
}

func (m Model) applyEvent(ev harness.Event) Model {
switch ev.Kind {
case harness.EvThinking:
if m.run.thinkAt.IsZero() {
m.run.thinkAt = time.Now()
}
m.chat = m.chat.EndWrite()
m.chat = m.chat.StreamThinking(ev.Text)

case harness.EvText:
m = m.endThinking()
m.chat = m.chat.EndWrite()
m.chat = m.chat.StreamAnswer(ev.Text)

case harness.EvUsage:
m.run.tokens = ev.Tokens
// Persist tokens to the current session
if id := m.ps.current(); id != "" && m.st != nil {
_ = m.st.UpdateSessionTokens(id, ev.Tokens)
}

case harness.EvToolProgress:
if ev.Progress != nil {
m = m.endThinking()
m.chat = m.chat.WriteProgress(ev.Progress.Name, ev.Progress.Path, ev.Progress.Creating)
}

case harness.EvToolCall:
m = m.endThinking()
m.chat = m.chat.EndWrite()
m.chat = m.chat.DemoteAnswer()
if ev.Call != nil {
if ev.Call.Name == "read_file" {
path, _ := ev.Call.Args["path"].(string)
m.chat = m.chat.ReadFile(path)
} else if ev.Call.Name == "edit_file" {
// Show the running state as "creating file (path)"
path, _ := ev.Call.Args["path"].(string)
create := ev.Call.Args["old_string"] == ""
m.chat = m.chat.WriteProgress("edit_file", path, create)
}
}

case harness.EvToolResult:
if ev.Call == nil || ev.Result == nil {
break
}
if ev.Call.Name == "edit_file" && !ev.Result.IsError {
newS, _ := tools.StringArg(ev.Call.Args, "new_string")
path, _ := tools.StringArg(ev.Call.Args, "path")
create := ev.Call.Args["old_string"] == ""

// Format it like a file block
verb := "Wrote"
if create {
verb = "Created"
}
m.chat = m.chat.EndWrite() // remove the "creating..." spinner
m.chat = m.chat.RunShell(verb+" "+path, newS)
break
}
// A successful read_file is already on screen from its call.
if ev.Call.Name != "read_file" || ev.Result.IsError {
out := ev.Result.Output
if ev.Result.IsError {
out = "Error: " + out
}
m.chat = m.chat.RunShell(callLabel(*ev.Call), out)
}
	if ev.Call.Name == "edit_file" && !ev.Result.IsError {
		// Extract the new content that was written
		newS, _ := tools.StringArg(ev.Call.Args, "new_string")
		path, _ := tools.StringArg(ev.Call.Args, "path")

		// Display it as a block, like the image
		title := "Wrote " + path
		m.chat = m.chat.RunShell(title, newS)
		break
	}
	// A successful read_file is already on screen from its call.
	if ev.Call.Name != "read_file" || ev.Result.IsError {
		out := ev.Result.Output
		if ev.Result.IsError {
			out = "Error: " + out
		}
		m.chat = m.chat.RunShell(callLabel(*ev.Call), out)
	}

case harness.EvDone:
m = m.endThinking()
m.chat = m.chat.EndWrite()
m.chat = m.chat.EndAnswer()
// The persister will save the whole message (including steps) when it records it
if m.ps != nil && m.ps.current() != "" {
// Wait for Tick to commit it
}

case harness.EvError:
msg := "request failed"
if ev.Err != nil {
msg = ev.Err.Error()
}
if m.cfg != nil && m.cfg.Log.File != "" {
msg += "  (log: " + m.cfg.Log.File + ")"
}
m.chat = m.chat.Fail(msg)
}
return m
}

func (m Model) endThinking() Model {
took := time.Second
if !m.run.thinkAt.IsZero() {
took = time.Since(m.run.thinkAt)
}
m.chat = m.chat.EndThinking(took)
return m
}

const maxDiffLines = 60

var firstLineRe = regexp.MustCompile(`first at line (\d+)`)

func editDisplay(c tools.Call, res *tools.Result) (path string, create bool, diff []components.DiffLine, ok bool) {
path, _ = tools.StringArg(c.Args, "path")
if strings.TrimSpace(path) == "" {
return "", false, nil, false
}
oldS, _ := tools.StringArg(c.Args, "old_string")
newS, _ := tools.StringArg(c.Args, "new_string")
create = strings.TrimSpace(oldS) == ""

start := 1
if !create {
if m := firstLineRe.FindStringSubmatch(res.Output); m != nil {
if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
start = n
}
}
for _, l := range textLines(oldS) {
diff = append(diff, components.DiffLine{Kind: components.DiffDel, Num: start, Text: l})
start++
}
}
for _, l := range textLines(newS) {
diff = append(diff, components.DiffLine{Kind: components.DiffAdd, Num: start, Text: l})
start++
}
if len(diff) > maxDiffLines {
more := len(diff) - maxDiffLines
diff = append(diff[:maxDiffLines], components.DiffLine{Text: fmt.Sprintf("… %d more lines", more)})
}
return path, create, diff, true
}

func textLines(s string) []string {
if s == "" {
return nil
}
return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

func callLabel(c tools.Call) string {
switch c.Name {
case "shell":
if cmd, _ := c.Args["command"].(string); strings.TrimSpace(cmd) != "" {
return clipLine(cmd)
}
case "edit_file":
if p, _ := c.Args["path"].(string); p != "" {
if old, _ := c.Args["old_string"].(string); old == "" {
return clipLine("create " + p)
}
return clipLine("edit " + p)
}
}
args, _ := json.Marshal(c.Args)
s := c.Name
if len(c.Args) > 0 {
s += " " + string(args)
}
return clipLine(s)
}

func clipLine(s string) string {
s = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s))
if r := []rune(s); len(r) > 100 {
s = string(r[:100]) + "…"
}
return s
}



