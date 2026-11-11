package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"kai/internal/config"
	"kai/internal/store"
	"kai/internal/ui/components"
	"kai/internal/version"
)

const contextWindow = 256_000

// maxEventBatch caps how many queued agent events are applied per redraw.
const maxEventBatch = 256
const sidebarMinTerm = 100
const dialogDim = 0.5

const (
	minW = 60
	minH = 14
)

type Screen int

const (
	ScreenHome Screen = iota
	ScreenChat
)

type AppState int

const (
	StateIdle AppState = iota
	StateThinking
)

type Model struct {
	width, height int
	mainW, sideW  int
	bodyH         int

	chat    components.ChatModel
	input   components.InputModel
	sidebar components.SidebarModel
	palette components.PaletteModel
	dialog  components.DialogModel
	run     runState

	sidebarOpen bool
	notice      string
	noticeSeq   int

	screen Screen
	state  AppState
	fake   *fakeStream
	cfg    *config.Config
	ready  bool

	st  *store.Store
	ps  *persister
	hub *providerHub

	picker     components.DialogModel
	pickerKind pickerKind
	pickerFor  int64
	form       components.FormModel
	formKind   formKind
	formFor    int64
}

func NewModel(cfg *config.Config, st *store.Store) Model {
	ps := newPersister(st, cwdLabel())
	hub := &providerHub{cfg: cfg, st: st}
	return Model{
		chat:        components.NewChat(80, 20),
		input:       components.NewInput(80).SetPlaceholder("Ask anything"),
		sidebar:     components.NewSidebar().SetSessionID("78393329910").SetVersion(version.Version).SetCwd(cwdLabel()),
		palette:     components.NewPalette(components.DefaultCommands()),
		dialog:      components.NewDialog(components.DefaultDialogItems()),
		cfg:         cfg,
		run:         newRunState(cfg, ps, hub),
		st:          st,
		ps:          ps,
		hub:         hub,
		screen:      ScreenHome,
		sidebarOpen: true,
		state:       StateIdle,
	}
}

func (m Model) Init() tea.Cmd { return nil }

func tick() tea.Cmd {
	return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) layout() Model {
	m.palette = m.palette.Sync(m.input.Value())
	m.dialog = m.dialog.SetSize(m.width, m.height)
	m.picker = m.picker.SetSize(m.width, m.height)
	m.form = m.form.SetSize(m.width, m.height)
	if m.width < minW || m.height < minH {
		return m
	}
	if m.screen == ScreenHome {
		inputW := m.width - 4
		if inputW > 64 {
			inputW = 64
		}
		m.palette = m.palette.SetRows(lipgloss.Height(components.Banner(version.Version)) + 1)
		m.input = m.input.SetWidth(inputW + 2*components.Margin)
		return m
	}

	sideW := 0
	if m.sidebarOpen && m.width >= sidebarMinTerm {
		sideW = m.width * 24 / 100
		if sideW < 16 {
			sideW = 16
		}
	}
	mainW := m.width - sideW
	m.mainW, m.sideW = mainW, sideW
	m.input = m.input.SetWidth(mainW)
	m.bodyH = m.height
	chatH := m.bodyH - m.input.Height() - 1
	if chatH < 1 {
		chatH = 1
	}
	m.chat = m.chat.SetSize(mainW, chatH)
	m.palette = m.palette.SetRows(chatH)
	m.sidebar = m.sidebar.SetSize(sideW, m.bodyH)
	return m
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m.layout(), nil

	case tickMsg:
		if !m.chat.Active() {
			m.state = StateIdle
			m.fake = nil
			return m, nil
		}
		if m.fake != nil {
			m.chat = m.fake.step(m.chat)
		}
		m.chat = m.chat.Tick()
		if !m.chat.Active() {
			m.state = StateIdle
			m.fake = nil
			return m, nil
		}
		return m, tick()

	case modelsMsg:
		nm, cmd := m.onModels(msg)
		return nm.layout(), cmd

	case tea.PasteMsg:
		if m.form.Visible() {
			m.form = m.form.Paste(msg.Content)
			return m, nil
		}
		if m.modalOpen() {
			return m, nil
		}
		var pcmd tea.Cmd
		m.input, pcmd = m.input.Update(msg)
		return m.layout(), pcmd

	case agentMsg:
		if msg.id != m.run.id {
			return m, nil
		}
		m = m.applyEvent(msg.ev)
		return m.layout(), waitEvent(m.run.id, m.run.events)

	case agentDoneMsg:
		return m, nil

	case noticeClearMsg:
		if msg.seq == m.noticeSeq {
			m.notice = ""
		}
		return m, nil

	case tea.ClipboardMsg:
		if m.form.Visible() {
			m.form = m.form.Paste(msg.Content)
			return m, nil
		}
		m.input, _ = m.input.Update(tea.PasteMsg{Content: msg.Content})
		return m.layout(), nil

	case tea.MouseMotionMsg:
		if m.modalOpen() {
			return m, nil
		}
		m.chat = m.chat.Hover(msg.X, msg.Y)
		if m.sideW > 0 {
			m.sidebar = m.sidebar.Hover(msg.X-m.mainW, msg.Y)
		}
		return m, nil

	case tea.MouseClickMsg:
		if m.modalOpen() {
			return m, nil
		}
		if msg.Button == tea.MouseLeft {
			m.chat = m.chat.Click(msg.X, msg.Y)
		}
		return m, nil

	case tea.MouseWheelMsg:
		if m.form.Visible() {
			return m, nil
		}
		if m.picker.Visible() {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.picker = m.picker.Scroll(-1)
			case tea.MouseWheelDown:
				m.picker = m.picker.Scroll(1)
			}
			return m, nil
		}
		if m.dialog.Visible() {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.dialog = m.dialog.Scroll(-1)
			case tea.MouseWheelDown:
				m.dialog = m.dialog.Scroll(1)
			}
			return m, nil
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			m.chat = m.chat.ScrollUp(3)
		case tea.MouseWheelDown:
			m.chat = m.chat.ScrollDown(3)
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.form.Visible() {
			if msg.String() == "ctrl+v" {
				return m, pasteCmd()
			}
			var fres components.FormResult
			m.form, fres = m.form.Update(msg)
			switch fres {
			case components.FormSubmitted:
				nm, cmd := m.submitForm()
				return nm.layout(), cmd
			case components.FormCancelled:
				nm, cmd := m.cancelForm()
				return nm.layout(), cmd
			}
			return m, nil
		}
		if m.picker.Visible() {
			var pres components.DialogResult
			m.picker, pres = m.picker.Update(msg)
			if pres == components.DialogChose {
				if it, ok := m.picker.Selected(); ok {
					nm, cmd := m.pickerChose(it)
					return nm.layout(), cmd
				}
			}
			return m, nil
		}
		if m.dialog.Visible() {
			var res components.DialogResult
			m.dialog, res = m.dialog.Update(msg)
			if res == components.DialogChose {
				if it, ok := m.dialog.Selected(); ok {
					return m.runAction(it.ID)
				}
			}
			return m, nil
		}
		if m.palette.Visible() {
			switch msg.String() {
			case "up", "ctrl+p":
				m.palette = m.palette.Up()
				return m, nil
			case "down":
				m.palette = m.palette.Down()
				return m, nil
			case "tab":
				if c, ok := m.palette.Selected(); ok {
					m.input = m.input.SetValue("/" + c.Name + " ")
				}
				return m.layout(), nil
			case "enter":
				if c, ok := m.palette.Selected(); ok {
					if nm, cmd, ran := m.runCommand(c.Name); ran {
						return nm.layout(), cmd
					}
					m.input = m.input.SetValue("/" + c.Name + " ")
				}
				return m.layout(), nil
			case "esc":
				m.palette = m.palette.Dismiss(m.input.Value())
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			if msg.String() == "esc" && m.chat.Active() {
				m = m.stopRun()
				m.chat = m.chat.Stop()
				m.state, m.fake = StateIdle, nil
				m = m.saveState()
				return m.layout(), nil
			}
			if m.input.Value() == "" {
				m = m.saveState()
				return m, tea.Quit
			}
			m.input = m.input.Reset()
			return m.layout(), nil

		case "ctrl+l":
			m.chat = m.chat.Clear()
			return m.layout(), nil

		case "ctrl+p", "ctrl+k":
			m.dialog = m.dialog.Open()
			return m, nil

		case "ctrl+d":
			m.sidebarOpen = !m.sidebarOpen
			return m.layout(), nil

		case "ctrl+n":
			return m.newSession().layout(), nil

		case "ctrl+v":
			return m, pasteCmd()

		case "pgup":
			m.chat = m.chat.ScrollUp(5)
			return m, nil
		case "pgdown":
			m.chat = m.chat.ScrollDown(5)
			return m, nil
		case "up":
			m.chat = m.chat.ScrollUp(1)
			return m, nil
		case "down":
			m.chat = m.chat.ScrollDown(1)
			return m, nil

		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if f := strings.Fields(text); len(f) > 0 && strings.HasPrefix(f[0], "/") {
				if nm, cmd, ran := m.runCommand(strings.TrimPrefix(f[0], "/")); ran {
					return nm.layout(), cmd
				}
			}
			if text == "" || m.state == StateThinking {
				return m, nil
			}
			m.screen = ScreenChat
			m.chat = m.chat.AppendUser(text).StartReply()
			m.input = m.input.Reset().SetPlaceholder("Type a message...")
			m.state = StateThinking
			var wait tea.Cmd
			m, wait = m.startRun(text)
			return m.layout(), tea.Batch(tick(), wait)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m.layout(), cmd
}

func (m Model) View() tea.View {
	if !m.ready {
		return tea.NewView("Initializing...")
	}

	var content string
	if m.width < minW || m.height < minH {
		content = fmt.Sprintf("Terminal too small (%dx%d). Need at least %dx%d.",
			m.width, m.height, minW, minH)
	} else if m.screen == ScreenHome {
		block := lipgloss.JoinVertical(lipgloss.Center,
			components.Banner(version.Version),
			"",
			m.input.View(),
			m.noticeRow(m.input.Width()),
		)
		inputTop := lipgloss.Height(block) - 1 - m.input.Height()
		popupX := (lipgloss.Width(block)-m.input.Width())/2 + components.Margin
		block = components.OverlayString(block, m.palette.View(m.input.Width()-2*components.Margin), popupX, inputTop)
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Position(0.52), block)
	} else {
		gap := m.noticeRow(m.mainW)
		mainCol := lipgloss.JoinVertical(lipgloss.Left,
			m.chat.View(),
			m.input.View(),
			gap,
		)
		mainCol = components.OverlayString(mainCol, m.palette.View(m.mainW-2*components.Margin), components.Margin, lipgloss.Height(m.chat.View()))
		if m.sideW > 0 {
			content = lipgloss.JoinHorizontal(lipgloss.Top,
				mainCol, m.sidebar.SetContext(m.run.tokens, contextWindow).SetProvider(m.hub.label).View(),
			)
		} else {
			content = mainCol
		}
	}

	content = m.overlayModal(content)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = components.ColBG
	v.MouseMode = tea.MouseModeAllMotion
	return v
}

// Shutdown saves the open session one last time after the TUI has stopped.
// The chat and the provider's token count are written to the database, so the
// next start shows the same timeline.
func (m Model) Shutdown() {
	m = m.stopRun()
	m = m.saveState()
}
