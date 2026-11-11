package ui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"kai/internal/harness"
	"kai/internal/store"
	"kai/internal/ui/components"
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
		s = string(r[:59]) + "�"
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
	// Save the full entry including steps when the caller has them.
	if err := p.st.AppendMessageSteps(p.id, e.Role, e.Text, e.Steps, e.At); err != nil {
		slog.Warn("could not save a message", "session", p.id, "err", err)
	}
}

// sessionState is everything needed to reopen a session exactly as it was:
// the rendered chat (timeline nodes included), the agent's native
// conversation, and the provider's real token count.
type sessionState struct {
	Messages []components.Message `json:"messages,omitempty"`
	Tokens   int                  `json:"tokens,omitempty"`
	Agent    harness.Snapshot     `json:"agent,omitempty"`
}

// saveState writes the current chat and agent state to the session. It is
// called when a run ends and when the session is switched or Kai exits, so a
// timeline never disappears between visits.
func (m Model) saveState() Model {
	if m.st == nil || m.ps == nil {
		return m
	}
	id := m.ps.current()
	if id == "" {
		return m // no message has been saved yet: nothing to attach it to
	}
	state := sessionState{
		Messages: m.chat.Snapshot(),
		Tokens:   m.run.tokens,
	}
	if a := m.agent(); a != nil {
		state.Agent = a.Snapshot()
	}
	b, err := json.Marshal(state)
	if err != nil {
		slog.Warn("could not encode the session state", "session", id, "err", err)
		return m
	}
	if err := m.st.SaveSessionState(id, string(b), m.run.tokens); err != nil {
		slog.Warn("could not save the session state", "session", id, "err", err)
		return m
	}
	m.run.savedTokens, m.run.savedAt = m.run.tokens, time.Now()
	return m
}

// loadSessionState reads a saved session state. ok is false when the session
// predates state saving or when the blob is unreadable.
func loadSessionState(st *store.Store, id string) (sessionState, bool) {
	if st == nil || id == "" {
		return sessionState{}, false
	}
	raw, err := st.SessionState(id)
	if err != nil || strings.TrimSpace(raw) == "" {
		return sessionState{}, false
	}
	var state sessionState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		slog.Warn("could not read the session state", "session", id, "err", err)
		return sessionState{}, false
	}
	return state, true
}
