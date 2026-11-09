package ui

import (
"crypto/rand"
"encoding/hex"
"fmt"
"log/slog"
"strings"
"sync"
"time"

"kai/internal/harness"
"kai/internal/store"
)

type persister struct {
mu       sync.Mutex
st       *store.Store
id       string
provider string
cwd      string
}

func newPersister(st *store.Store, cwd string) *persister {
return &persister{st: st, cwd: cwd}
}

func newStoreID() string {
b := make([]byte, 6)
if _, err := rand.Read(b); err != nil {
return fmt.Sprintf("s%d", time.Now().UnixNano())
}
return hex.EncodeToString(b)
}

func titleFrom(s string) string {
s = strings.Join(strings.Fields(s), " ")
if r := []rune(s); len(r) > 60 {
s = string(r[:59]) + "…"
}
return s
}

func (p *persister) current() string {
if p == nil {
return ""
}
p.mu.Lock()
defer p.mu.Unlock()
return p.id
}

func (p *persister) begin() {
if p == nil {
return
}
p.mu.Lock()
p.id = ""
p.mu.Unlock()
}

func (p *persister) use(id string) {
if p == nil {
return
}
p.mu.Lock()
p.id = id
p.mu.Unlock()
}

func (p *persister) setProvider(label string) {
if p == nil {
return
}
p.mu.Lock()
p.provider = label
id := p.id
p.mu.Unlock()
if id != "" && p.st != nil {
if err := p.st.TouchSession(id, label); err != nil {
slog.Warn("could not update the session's provider", "err", err)
}
}
}

func (p *persister) record(e harness.Entry) {
if p == nil || p.st == nil {
return
}
p.mu.Lock()
defer p.mu.Unlock()
if p.id == "" {
p.id = newStoreID()
title := ""
if e.Role == "user" {
title = titleFrom(e.Text)
}
err := p.st.CreateSession(store.Session{ID: p.id, Title: title, Provider: p.provider, Cwd: p.cwd, CreatedAt: e.At})
if err != nil {
slog.Warn("could not create the session", "err", err)
}
}
if err := p.st.AppendMessage(p.id, e.Role, e.Text, e.At); err != nil {
slog.Warn("could not save a message", "session", p.id, "err", err)
}
}

