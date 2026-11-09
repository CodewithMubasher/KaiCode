package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// runCommand runs a slash command by name. ran is false for commands that
// are not implemented yet (the caller then just completes the text).
func (m Model) runCommand(name string) (nm Model, cmd tea.Cmd, ran bool) {
	switch name {
	case "new":
		return m.newSession(), nil, true
	case "clear":
		m = m.stopRun()
		m.run.agent.Reset(context.Background())
		m.run.tokens = 0
		m.chat = m.chat.Clear()
		m.input = m.input.Reset()
		m.state, m.fake = StateIdle, nil
		return m, nil, true
	case "copy":
		m.input = m.input.Reset()
		nm, cmd = m.copyLast()
		return nm, cmd, true
	case "exit":
		return m, tea.Quit, true
	case "commands":
		m.input = m.input.Reset()
		m.dialog = m.dialog.Open()
		return m, nil, true
	}
	return m, nil, false
}

// newSession drops the conversation and goes back to the home screen.
func (m Model) newSession() Model {
	m = m.stopRun()
	m.run.agent.Reset(context.Background())
	m.run.tokens = 0
	m.chat = m.chat.Clear()
	m.input = m.input.Reset().SetPlaceholder("Ask anything")
	m.screen = ScreenHome
	m.state, m.fake = StateIdle, nil
	m.notice = ""
	return m
}

// copyLast copies the last AI answer (as markdown) to the clipboard.
func (m Model) copyLast() (Model, tea.Cmd) {
	text := m.chat.LastAnswer()
	if text == "" {
		return m.setNotice("Nothing to copy yet")
	}
	m, clear := m.setNotice("✓ Copied last response")
	return m, tea.Batch(tea.SetClipboard(text), copyToClipboard(text), clear)
}

// runAction runs an action chosen in the command dialog.
func (m Model) runAction(id string) (tea.Model, tea.Cmd) {
	switch id {
	case "sidebar":
		m.sidebarOpen = !m.sidebarOpen
		return m.layout(), nil
	case "paste":
		return m, pasteCmd()
	}
	if nm, cmd, ran := m.runCommand(id); ran {
		return nm.layout(), cmd
	}
	nm, cmd := m.setNotice("Not implemented yet")
	return nm, cmd
}
