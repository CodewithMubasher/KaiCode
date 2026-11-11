package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"kai/internal/bridge"
	"kai/internal/config"
	"kai/internal/harness"
	"kai/internal/harness/provider/openai"
	"kai/internal/store"
	"kai/internal/ui/components"
)

// settingActive is the settings key holding the provider in use:
// "web" (DeepSeek Web), "qwen" (Qwen Web; both through the Chrome extension
// bridge) or "custom:<id>".
const settingActive = "active_provider"

var errNoModel = errors.New("no model selected")

type pickerKind int

const (
	pickerNone pickerKind = iota
	pickerProviders
	pickerRemoveProvider
	pickerModels
	pickerSessions
	pickerDeleteSession
)

type formKind int

const (
	formNone formKind = iota
	formAddProvider
	formModelName
	formRename
)

// activeProvider says which provider answers.
type activeProvider struct {
	Kind string // "web" (DeepSeek), "qwen", "mock" or "custom"
	ID   int64  // custom provider id
}

func (a activeProvider) String() string {
	if a.Kind == "custom" {
		return "custom:" + strconv.FormatInt(a.ID, 10)
	}
	return a.Kind
}

func parseActive(s string) activeProvider {
	if rest, ok := strings.CutPrefix(s, "custom:"); ok {
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil && id > 0 {
			return activeProvider{Kind: "custom", ID: id}
		}
	}
	if s == "qwen" {
		return activeProvider{Kind: "qwen"}
	}
	return activeProvider{Kind: "web"}
}

// providerHub builds providers. The Chrome bridge opens a local port, so it is
// started only when it is actually chosen.
type providerHub struct {
	cfg    *config.Config
	st     *store.Store
	srv    *bridge.Server // the Chrome bridge, started the first time a web chat is chosen
	srvErr error
	active activeProvider
	label  string
}

// site returns the web chat provider for one site, starting the bridge once.
// DeepSeek and Qwen share the one bridge (one port, one extension).
func (h *providerHub) site(site string) harness.Provider {
	if h.srv == nil && h.srvErr == nil {
		h.srv, h.srvErr = startBridge(h.cfg)
	}
	if h.srvErr != nil {
		return brokenProvider{h.srvErr}
	}
	if site == bridge.SiteDeepSeek {
		return h.srv
	}
	return h.srv.ForSite(site)
}

// build returns the backend for a choice. DeepSeek Web, Qwen Web (and the
// scripted demo) use the text-protocol path; Qwen Web speaks JSON, DeepSeek
// Web speaks DSML. A custom provider (including the Qwen API preset) is the
// native tool-calling path. A Backend holds one or the other, never both.
func (h *providerHub) build(a activeProvider) (harness.Backend, string, error) {
	switch a.Kind {
	case "mock":
		return harness.Backend{Web: &harness.MockProvider{Script: harness.DemoScript, ChunkRunes: 4, Delay: 15 * time.Millisecond}}, "Demo (scripted)", nil
	case "custom":
		if h.st == nil {
			return harness.Backend{}, "", errors.New("storage is unavailable")
		}
		p, err := h.st.ProviderByID(a.ID)
		if err != nil {
			return harness.Backend{}, "", fmt.Errorf("that provider no longer exists")
		}
		if strings.TrimSpace(p.Model) == "" {
			return harness.Backend{}, "", errNoModel
		}
		prov := openai.New(openai.Config{Name: p.Name, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model})
		return harness.Backend{Native: prov}, p.Name + " · " + p.Model, nil
	case "qwen":
		return harness.Backend{Web: h.site(bridge.SiteQwen)}, "Qwen Web", nil
	default:
		return harness.Backend{Web: h.site(bridge.SiteDeepSeek)}, "DeepSeek Web", nil
	}
}

// modelsMsg is the answer to a model-list request.
type modelsMsg struct {
	p      store.Provider
	models []string
	err    error
}

func fetchModelsCmd(p store.Provider) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		ms, err := openai.ListModels(ctx, p.BaseURL, p.APIKey)
		return modelsMsg{p: p, models: ms, err: err}
	}
}

func hostOfURL(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Host
	}
	return ""
}

// runProviderAction handles the command-menu actions about providers and
// sessions. ran is false for ids it does not know.
func (m Model) runProviderAction(id string) (Model, tea.Cmd, bool) {
	var cmd tea.Cmd
	switch id {
	case "provider_add":
		m, cmd = m.openAddProvider()
	case "providers":
		m, cmd = m.openProviderPicker()
	case "provider_remove":
		m, cmd = m.openRemovePicker()
	case "model":
		m, cmd = m.switchModel()
	case "sessions":
		m, cmd = m.openSessionPicker(pickerSessions)
	case "session_delete":
		m, cmd = m.openSessionPicker(pickerDeleteSession)
	case "session_rename":
		m, cmd = m.openRename()
	default:
		return m, nil, false
	}
	return m, cmd, true
}

func (m Model) noStorage() (Model, tea.Cmd) {
	return m.setNotice("Storage is unavailable (see logs.txt)")
}

// ---- add provider ----------------------------------------------------------

func (m Model) openAddProvider() (Model, tea.Cmd) {
	if m.st == nil {
		return m.noStorage()
	}
	m.form = components.NewForm("Add provider", []components.FormField{
		{Label: "Name", Placeholder: "e.g. OpenRouter"},
		{Label: "Base URL", Placeholder: "https://api.openai.com/v1"},
		{Label: "API key", Placeholder: "sk-...  (empty for local servers)", Secret: true},
		{Label: "Model", Placeholder: "optional: leave empty to pick from the provider's list"},
	}).SetSize(m.width, m.height)
	m.formKind = formAddProvider
	return m, nil
}

// openQwenAPI opens the built-in Qwen API preset. It is the native
// OpenAI-compatible path (DashScope), so Qwen gets real structured tool calls
// instead of the text protocol Qwen Web needs.
func (m Model) openQwenAPI() (Model, tea.Cmd) {
	if m.st == nil {
		return m.noStorage()
	}
	m.form = components.NewForm("Qwen API (native tool calling)", []components.FormField{
		{Label: "Name", Value: "Qwen API"},
		{Label: "Base URL", Value: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		{Label: "API key", Placeholder: "sk-... from dashscope.console.aliyun.com", Secret: true},
		{Label: "Model", Value: "qwen-plus", Placeholder: "qwen-plus, qwen-max, qwen3-coder-plus…"},
	}).SetSize(m.width, m.height).SetNote("DashScope's OpenAI-compatible endpoint: real tools/tool_calls, no text markup.")
	m.formKind = formAddProvider
	return m, nil
}

func (m Model) submitForm() (Model, tea.Cmd) {
	switch m.formKind {
	case formAddProvider:
		return m.submitAddProvider()
	case formModelName:
		return m.submitModelName()
	case formRename:
		return m.submitRename()
	}
	m.form, m.formKind = m.form.Close(), formNone
	return m, nil
}

func (m Model) cancelForm() (Model, tea.Cmd) {
	k := m.formKind
	m.form, m.formKind = m.form.Close(), formNone
	if k == formModelName {
		return m.setNotice("Provider saved. Pick a model with Switch model to use it")
	}
	return m, nil
}

func (m Model) submitAddProvider() (Model, tea.Cmd) {
	v := m.form.Values()
	name, base, key, model := v[0], v[1], v[2], v[3]
	switch {
	case name == "":
		m.form = m.form.SetError("Name is required")
		return m, nil
	case len([]rune(name)) > 40:
		m.form = m.form.SetError("Name is too long (40 characters at most)")
		return m, nil
	}
	norm, err := openai.NormalizeBaseURL(base)
	if err != nil {
		m.form = m.form.SetError(err.Error())
		return m, nil
	}
	p, err := m.st.AddProvider(store.Provider{Name: name, BaseURL: norm, APIKey: key, Model: model})
	if errors.Is(err, store.ErrDuplicate) {
		m.form = m.form.SetError("A provider named \"" + name + "\" already exists")
		return m, nil
	}
	if err != nil {
		slog.Error("could not save the provider", "err", err)
		m.form = m.form.SetError("Could not save: " + err.Error())
		return m, nil
	}
	slog.Info("provider added", "name", p.Name, "host", hostOfURL(p.BaseURL), "model", p.Model)
	m.form, m.formKind = m.form.Close(), formNone
	return m.activate(activeProvider{Kind: "custom", ID: p.ID})
}

// ---- switching -------------------------------------------------------------

// activate makes a provider the one that answers. The conversation continues:
// the next message opens a fresh chat on the new provider with a recap.
func (m Model) activate(a activeProvider) (Model, tea.Cmd) {
	backend, label, err := m.hub.build(a)
	if errors.Is(err, errNoModel) {
		return m.chooseModel(a.ID)
	}
	if err != nil {
		slog.Warn("could not switch provider", "provider", a.String(), "err", err)
		return m.setNotice("Could not switch: " + err.Error())
	}
	if m.chat.Active() {
		m = m.stopRun()
		m.chat = m.chat.Stop()
		m.state, m.fake = StateIdle, nil
	}
	m.run.agent.SetBackend(backend)
	// Keep the session's token count: switching provider must not make the
	// gauge look like a brand-new conversation. The new provider replaces it
	// with its own real count on the next turn.
	m.hub.active, m.hub.label = a, label
	m.ps.setProvider(label)
	m = m.saveState()
	if m.st != nil {
		if err := m.st.SetSetting(settingActive, a.String()); err != nil {
			slog.Warn("could not remember the provider", "err", err)
		}
	}
	slog.Info("provider active", "label", label)
	return m.setNotice("✓ Using " + label)
}

func (m Model) openProviderPicker() (Model, tea.Cmd) {
	items := []components.DialogItem{
		{Group: "Built-in", Title: "DeepSeek Web (Chrome extension)", Key: activeKey(m.hub.active.Kind == "web"), ID: "use:web"},
		{Group: "Built-in", Title: "Qwen Web (Chrome extension)", Key: activeKey(m.hub.active.Kind == "qwen"), ID: "use:qwen"},
		{Group: "Built-in", Title: "Qwen API (native tool calling)…", ID: "qwenapi"},
	}
	custom := 0
	if m.st != nil {
		list, err := m.st.Providers()
		if err != nil {
			slog.Warn("could not list providers", "err", err)
		}
		for _, p := range list {
			custom++
			title := p.Name
			if p.Model != "" {
				title += " · " + p.Model
			}
			key := hostOfURL(p.BaseURL)
			if m.hub.active.Kind == "custom" && m.hub.active.ID == p.ID {
				key = "active"
			}
			items = append(items, components.DialogItem{Group: "Custom", Title: title, Key: key, ID: "use:" + strconv.FormatInt(p.ID, 10)})
		}
	}
	items = append(items, components.DialogItem{Group: "Manage", Title: "Add provider…", ID: "add"})
	if custom > 0 {
		items = append(items, components.DialogItem{Group: "Manage", Title: "Remove provider…", ID: "remove"})
	}
	m.picker = components.NewDialog(items).WithTitle("Switch provider").SetSize(m.width, m.height).Open()
	m.pickerKind = pickerProviders
	return m, nil
}

func activeKey(on bool) string {
	if on {
		return "active"
	}
	return ""
}

func (m Model) openRemovePicker() (Model, tea.Cmd) {
	if m.st == nil {
		return m.noStorage()
	}
	list, err := m.st.Providers()
	if err != nil || len(list) == 0 {
		return m.setNotice("No custom providers to remove")
	}
	var items []components.DialogItem
	for _, p := range list {
		items = append(items, components.DialogItem{Group: "Custom providers", Title: p.Name, Key: hostOfURL(p.BaseURL), ID: "r:" + strconv.FormatInt(p.ID, 10)})
	}
	m.picker = components.NewDialog(items).WithTitle("Remove provider").SetSize(m.width, m.height).Open()
	m.pickerKind = pickerRemoveProvider
	return m, nil
}

// ---- models ----------------------------------------------------------------

func (m Model) switchModel() (Model, tea.Cmd) {
	if m.hub.active.Kind != "custom" {
		return m.setNotice("The web chat's model is chosen in the browser")
	}
	return m.chooseModel(m.hub.active.ID)
}

// chooseModel asks the provider which models it has and shows them.
func (m Model) chooseModel(id int64) (Model, tea.Cmd) {
	if m.st == nil {
		return m.noStorage()
	}
	p, err := m.st.ProviderByID(id)
	if err != nil {
		return m.setNotice("That provider no longer exists")
	}
	m, clear := m.setNotice("Fetching models from " + p.Name + "…")
	return m, tea.Batch(clear, fetchModelsCmd(p))
}

func (m Model) onModels(msg modelsMsg) (Model, tea.Cmd) {
	if msg.err != nil || len(msg.models) == 0 {
		slog.Warn("could not list models", "provider", msg.p.Name, "err", msg.err)
		hint := "Couldn't list models. Type the model name"
		if msg.err != nil {
			hint = "Couldn't list models (" + clipLine(msg.err.Error()) + "). Type the model name"
		}
		return m.openModelForm(msg.p, hint)
	}
	var items []components.DialogItem
	for i, id := range msg.models {
		if i >= 300 {
			break
		}
		key := ""
		if id == msg.p.Model {
			key = "current"
		}
		items = append(items, components.DialogItem{Group: "Models", Title: id, Key: key, ID: "m:" + id})
	}
	items = append(items, components.DialogItem{Group: "Other", Title: "Type a model name…", ID: "manual"})
	m.picker = components.NewDialog(items).WithTitle("Model · "+msg.p.Name).SetSize(m.width, m.height).Open()
	m.pickerKind, m.pickerFor = pickerModels, msg.p.ID
	return m, nil
}

func (m Model) openModelForm(p store.Provider, note string) (Model, tea.Cmd) {
	m.form = components.NewForm("Model · "+p.Name, []components.FormField{
		{Label: "Model name", Placeholder: "e.g. gpt-4o-mini", Value: p.Model},
	}).SetSize(m.width, m.height).SetNote(note)
	m.formKind, m.formFor = formModelName, p.ID
	return m, nil
}

func (m Model) submitModelName() (Model, tea.Cmd) {
	name := m.form.Values()[0]
	if name == "" {
		m.form = m.form.SetError("Model name is required")
		return m, nil
	}
	id := m.formFor
	if err := m.st.SetProviderModel(id, name); err != nil {
		m.form = m.form.SetError("Could not save: " + err.Error())
		return m, nil
	}
	m.form, m.formKind = m.form.Close(), formNone
	return m.activate(activeProvider{Kind: "custom", ID: id})
}

// ---- picker choice ---------------------------------------------------------

func (m Model) pickerChose(it components.DialogItem) (Model, tea.Cmd) {
	kind := m.pickerKind
	m.picker, m.pickerKind = m.picker.Close(), pickerNone
	switch kind {
	case pickerProviders:
		switch {
		case it.ID == "add":
			return m.openAddProvider()
		case it.ID == "remove":
			return m.openRemovePicker()
		case it.ID == "use:web":
			return m.activate(activeProvider{Kind: "web"})
		case it.ID == "use:qwen":
			return m.activate(activeProvider{Kind: "qwen"})
		case it.ID == "qwenapi":
			return m.openQwenAPI()
		case strings.HasPrefix(it.ID, "use:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(it.ID, "use:"), 10, 64)
			if err == nil {
				return m.activate(activeProvider{Kind: "custom", ID: id})
			}
		}
	case pickerRemoveProvider:
		id, err := strconv.ParseInt(strings.TrimPrefix(it.ID, "r:"), 10, 64)
		if err != nil {
			break
		}
		wasActive := m.hub.active.Kind == "custom" && m.hub.active.ID == id
		if err := m.st.DeleteProvider(id); err != nil {
			return m.setNotice("Could not remove the provider")
		}
		if wasActive {
			return m.activate(activeProvider{Kind: "web"})
		}
		return m.setNotice("✓ Provider removed")
	case pickerModels:
		if it.ID == "manual" {
			p, err := m.st.ProviderByID(m.pickerFor)
			if err != nil {
				return m.setNotice("That provider no longer exists")
			}
			return m.openModelForm(p, "")
		}
		model := strings.TrimPrefix(it.ID, "m:")
		if err := m.st.SetProviderModel(m.pickerFor, model); err != nil {
			return m.setNotice("Could not save the model")
		}
		return m.activate(activeProvider{Kind: "custom", ID: m.pickerFor})
	case pickerSessions:
		return m.loadSession(strings.TrimPrefix(it.ID, "s:"))
	case pickerDeleteSession:
		return m.deleteSession(strings.TrimPrefix(it.ID, "s:"))
	}
	return m, nil
}
