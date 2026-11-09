package ui

import (
"fmt"
"strings"
"time"

tea "charm.land/bubbletea/v2"
"charm.land/lipgloss/v2"

"kai/internal/config"
"kai/internal/ui/components"
"kai/internal/version"
)

// contextWindow is the model context size the sidebar measures against.
const contextWindow = 256_000

const (
minW = 60
minH = 14
)

// Screen is which top-level screen is showing.
type Screen int

const (
ScreenHome Screen = iota // banner + centered input, until the first message
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

screen Screen
state  AppState
fake   *fakeStream
cfg   *config.Config
ready bool
}

func NewModel(cfg *config.Config) Model {
return Model{
chat:    components.NewChat(80, 20),
input:   components.NewInput(80).SetPlaceholder("Ask anything"),
sidebar: components.NewSidebar().SetSessionID("78393329910").SetVersion(version.Version).SetCwd(cwdLabel()), // TODO: real session id
palette: components.NewPalette(components.DefaultCommands()),
cfg:     cfg,
screen:  ScreenHome,
state:   StateIdle,
}
}

func (m Model) Init() tea.Cmd { return nil }

func tick() tea.Cmd {
return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// layout recomputes every component size from the terminal size.
func (m Model) layout() Model {
// the slash-command popup follows whatever is typed in the input
m.palette = m.palette.Sync(m.input.Value())
if m.width < minW || m.height < minH {
return m
}
if m.screen == ScreenHome {
inputW := m.width - 4
if inputW > 64 {
inputW = 64
}
// the popup may cover the banner and the gap above the input
m.palette = m.palette.SetRows(lipgloss.Height(components.Banner(version.Version)) + 1)
m.input = m.input.SetWidth(inputW + 2*components.Margin)
return m
}

sideW := m.width * 24 / 100
if sideW < 16 {
sideW = 16
}
mainW := m.width - sideW
m.mainW, m.sideW = mainW, sideW

m.input = m.input.SetWidth(mainW)

m.bodyH = m.height
chatH := m.bodyH - m.input.Height() - 1    // 1 gap row under input
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

case tea.MouseMotionMsg:
m.chat = m.chat.Hover(msg.X, msg.Y)
m.sidebar = m.sidebar.Hover(msg.X-m.mainW, msg.Y)
return m, nil

case tea.MouseClickMsg:
if msg.Button == tea.MouseLeft {
m.chat = m.chat.Click(msg.X, msg.Y)
}
return m, nil

case tea.MouseWheelMsg:
switch msg.Button {
case tea.MouseWheelUp:
m.chat = m.chat.ScrollUp(3)
case tea.MouseWheelDown:
m.chat = m.chat.ScrollDown(3)
}
return m, nil

case tea.KeyPressMsg:
if m.palette.Visible() {
switch msg.String() {
case "up", "ctrl+p":
m.palette = m.palette.Up()
return m, nil
case "down", "ctrl+n":
m.palette = m.palette.Down()
return m, nil
case "tab", "enter":
if c, ok := m.palette.Selected(); ok {
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
if m.input.Value() == "" {
return m, tea.Quit
}
m.input = m.input.Reset()
return m.layout(), nil

case "ctrl+l":
m.chat = m.chat.Clear()
return m.layout(), nil

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
if text == "" || m.state == StateThinking {
return m, nil
}
m.screen = ScreenChat
m.chat = m.chat.AppendUser(text).StartReply()
m.input = m.input.Reset().SetPlaceholder("Type a message...")
m.state = StateThinking
m.fake = newFakeStream()
return m.layout(), tick()
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
)
// slash-command popup sits right above the input
inputTop := lipgloss.Height(block) - m.input.Height()
popupX := (lipgloss.Width(block)-m.input.Width())/2 + components.Margin
block = components.OverlayString(block, m.palette.View(m.input.Width()-2*components.Margin), popupX, inputTop)
content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Position(0.52), block)
} else {
gap := strings.Repeat(" ", m.mainW)
mainCol := lipgloss.JoinVertical(lipgloss.Left,
m.chat.View(),
m.input.View(),
gap,
)
// slash-command popup sits right above the input
mainCol = components.OverlayString(mainCol, m.palette.View(m.mainW-2*components.Margin), components.Margin, lipgloss.Height(m.chat.View()))
content = lipgloss.JoinHorizontal(lipgloss.Top,
mainCol, m.sidebar.SetContext(m.chat.Tokens(), contextWindow).View(),
)
}

v := tea.NewView(content)
v.AltScreen = true
v.BackgroundColor = components.ColBG
v.MouseMode = tea.MouseModeAllMotion
return v
}
