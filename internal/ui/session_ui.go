package ui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"kai/internal/harness"
	"kai/internal/ui/components"
)

const sessionListLimit = 100

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2")
}

func (m Model) openSessionPicker(kind pickerKind) (Model, tea.Cmd) {
	if m.st == nil {
		return m.noStorage()
	}
	list, err := m.st.Sessions(sessionListLimit)
	if err != nil {
		slog.Warn("could not list sessions", "err", err)
		return m.setNotice("Could not read the saved sessions")
	}
	if len(list) == 0 {
		return m.setNotice("No saved sessions yet")
	}
	current := m.ps.current()
	var items []components.DialogItem
	for _, s := range list {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		key := ago(s.UpdatedAt)
		if s.ID == current {
			key = "current"
		}
		items = append(items, components.DialogItem{Group: "Sessions", Title: title, Key: key, ID: "s:" + s.ID})
	}
	heading := "Switch session"
	if kind == pickerDeleteSession {
		heading = "Delete session"
	}
	m.picker = components.NewDialog(items).WithTitle(heading).SetSize(m.width, m.height).Open()
	m.pickerKind = kind
	return m, nil
}

func (m Model) loadSession(id string) (Model, tea.Cmd) {
	// Save what is on screen first: switching away must never lose the
	// timeline of the session being left.
	m = m.saveState()

	msgs, err := m.st.Messages(id)
	if err != nil {
		slog.Warn("could not load the session", "session", id, "err", err)
		return m.setNotice("Could not load that session")
	}

	sess, err := m.st.Session(id)
	if err == nil {
		m.run.tokens = sess.Tokens
	} else {
		m.run.tokens = 0
	}
	state, hasState := loadSessionState(m.st, id)

	m = m.stopRun()
	var entries []harness.Entry
	for _, x := range msgs {
		entries = append(entries, harness.Entry{Role: x.Role, Text: x.Text, At: x.At})
	}
	if hasState {
		m.run.agent.ResumeWithState(entries, state.Agent)
	} else {
		m.run.agent.Resume(entries)
	}

	m.chat = m.chat.Clear()
	restored := false
	if hasState && len(state.Messages) > 0 {
		m.chat = m.chat.Restore(state.Messages)
		restored = true
	}
	if !restored {
		// A session saved by an older version has no state blob: rebuild the
		// chat from the messages, step data included when it was saved.
		var saved []components.Message
		for _, x := range msgs {
			switch x.Role {
			case "user":
				saved = append(saved, components.Message{Role: components.RoleUser, Text: x.Text})
			case "assistant":
				var steps []components.Step
				if x.Steps != "" {
					_ = json.Unmarshal([]byte(x.Steps), &steps)
				}
				saved = append(saved, components.Message{Role: components.RoleAssistant, Text: x.Text, Steps: steps})
			}
		}
		m.chat = m.chat.Restore(saved)
	}
	if state.Tokens > 0 {
		m.run.tokens = state.Tokens
	}
	m.run.savedTokens = m.run.tokens
	m.ps.use(id)
	m.screen = ScreenChat
	m.state, m.fake = StateIdle, nil
	m.input = m.input.Reset().SetPlaceholder("Type a message...")
	slog.Info("session switched", "session", id, "messages", len(entries), "timeline", restored)
	return m.setNotice("? Switched session")
}

func (m Model) deleteSession(id string) (Model, tea.Cmd) {
	if err := m.st.DeleteSession(id); err != nil {
		return m.setNotice("Could not delete that session")
	}
	if id == m.ps.current() {
		m.ps.begin() // the deleted id must not be saved again by newSession
		m = m.newSession()
	}
	return m.setNotice("? Session deleted")
}

func (m Model) openRename() (Model, tea.Cmd) {
	if m.st == nil {
		return m.noStorage()
	}
	id := m.ps.current()
	if id == "" {
		return m.setNotice("Nothing to rename yet: send a message first")
	}
	s, err := m.st.Session(id)
	if err != nil {
		return m.setNotice("Could not read this session")
	}
	m.form = components.NewForm("Rename session", []components.FormField{
		{Label: "Title", Placeholder: "Session title", Value: s.Title},
	}).SetSize(m.width, m.height)
	m.formKind, m.formFor = formRename, 0
	return m, nil
}

func (m Model) submitRename() (Model, tea.Cmd) {
	title := m.form.Values()[0]
	if title == "" {
		m.form = m.form.SetError("Title cannot be empty")
		return m, nil
	}
	if err := m.st.RenameSession(m.ps.current(), title); err != nil {
		m.form = m.form.SetError("Could not save: " + err.Error())
		return m, nil
	}
	m.form, m.formKind = m.form.Close(), formNone
	return m.setNotice("? Session renamed")
}
