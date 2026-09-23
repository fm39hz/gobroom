package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/fm39hz/gobroom/internal/daemon"
)

type sectionID int

const (
	sectionStatus sectionID = iota
	sectionProviders
	sectionConnections
	sectionModels
	sectionHealth
	sectionQuota
	sectionUsage
	sectionDiscovered
	sectionPhysical
	sectionComboModels
)

type section struct {
	id     sectionID
	label  string
	method string
}

var sections = []section{
	{id: sectionStatus, label: "Status", method: "status"},
	{id: sectionProviders, label: "Providers", method: "providers.list"},
	{id: sectionConnections, label: "Connections", method: "connections.list"},
	{id: sectionModels, label: "Model sources", method: "models.list"},
	{id: sectionHealth, label: "Health", method: "health.list"},
	{id: sectionQuota, label: "Quota", method: "quota.list"},
	{id: sectionUsage, label: "Usage", method: "usage.list"},
	{id: sectionDiscovered, label: "Discovered", method: "discovered_models.list"},
	{id: sectionPhysical, label: "Physical", method: "physical_models.list"},
	{id: sectionComboModels, label: "Combos", method: "combo_models.list"},
}

type dashboardPaneID int

const (
	dashboardStatus dashboardPaneID = iota
	dashboardConnections
	dashboardModels
	dashboardRuntime
)

type dashboardPaneDef struct {
	id      dashboardPaneID
	key     string
	label   string
	sources []sectionID
	view    string
}

type dashboardBlockDef struct {
	key, label string
	tabs       []dashboardPaneDef
}

// Blocks group related management tabs. Each flattened tab owns its own list
// model so filtering, cursor position and selection survive tab changes.
var dashboardPanes = []dashboardBlockDef{
	{key: "1", label: "Status", tabs: []dashboardPaneDef{{id: dashboardStatus, key: "status", label: "Status", sources: []sectionID{sectionStatus}, view: "status"}}},
	{key: "2", label: "Providers", tabs: []dashboardPaneDef{
		{id: dashboardConnections, key: "providers", label: "Providers", sources: []sectionID{sectionProviders}, view: "providers"},
		{id: dashboardConnections, key: "connections", label: "Connections", sources: []sectionID{sectionProviders, sectionConnections}, view: "connections"},
	}},
	{key: "3", label: "Models", tabs: []dashboardPaneDef{
		{id: dashboardModels, key: "discovered", label: "Discovered", sources: []sectionID{sectionDiscovered}, view: "discovered"},
		{id: dashboardModels, key: "physical", label: "Physical", sources: []sectionID{sectionPhysical, sectionDiscovered, sectionComboModels}, view: "physical"},
		{id: dashboardModels, key: "combos", label: "Combos", sources: []sectionID{sectionPhysical, sectionDiscovered, sectionComboModels}, view: "combos"},
	}},
	{key: "4", label: "Usage", tabs: []dashboardPaneDef{
		{id: dashboardRuntime, key: "usage", label: "Usage", sources: []sectionID{sectionUsage}, view: "usage"},
		{id: dashboardRuntime, key: "quota", label: "Quota", sources: []sectionID{sectionQuota}, view: "quota"},
	}},
	{key: "5", label: "Runtime", tabs: []dashboardPaneDef{
		{id: dashboardRuntime, key: "health", label: "Health", sources: []sectionID{sectionHealth}, view: "health"},
		{id: dashboardRuntime, key: "logs", label: "Logs", view: "unavailable-logs"},
	}},
}

type appMode uint8

const (
	modeBrowse appMode = iota
	modeForm
	modeCommand
	modeConfirm
	modeHelp
	modeSourcePicker
)

type focusPane uint8

const (
	focusDashboard focusPane = iota
	focusInspector
)

type dashboardPanel struct {
	definition dashboardPaneDef
	list       list.Model
	items      []entry
	selected   map[string]bool
	loading    bool
	err        string
}

type fetchMsg struct {
	section sectionID
	result  json.RawMessage
	err     error
}

type actionMsg struct {
	method string
	result json.RawMessage
	err    error
}

type keyHelp struct{ app *app }

func (h keyHelp) shortHelp() []key.Binding {
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("h/l"), key.WithHelp("h/l", "previous/next block")),
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "move in pane")),
		key.NewBinding(key.WithKeys("[/]"), key.WithHelp("[/]", "previous/next tab")),
	}
	if h.app == nil {
		return bindings
	}
	if h.app.focus == focusInspector {
		bindings := []key.Binding{
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "return")),
		}
		switch h.app.active().definition.view {
		case "providers":
			bindings = append(bindings, key.NewBinding(key.WithKeys("e/t/c"), key.WithHelp("e/t/c", "edit/test/connect")))
		case "connections":
			bindings = append(bindings, key.NewBinding(key.WithKeys("e/d"), key.WithHelp("e/d", "edit/delete")))
		case "discovered":
			bindings = append(bindings, key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "create Physical")))
		case "physical", "combos":
			bindings = append(bindings, key.NewBinding(key.WithKeys("e/p/d"), key.WithHelp("e/p/d", "edit/expose/delete")))
		}
		return append(bindings,
			key.NewBinding(key.WithKeys("H/L"), key.WithHelp("H/L", "scroll horizontally")),
			key.NewBinding(key.WithKeys("+/_"), key.WithHelp("+/_", "screen mode")),
			key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keybindings")),
		)
	}
	bindings = append(bindings, key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open main")))
	switch h.app.active().definition.view {
	case "providers":
		bindings = append(bindings, key.NewBinding(key.WithKeys("n/t"), key.WithHelp("n/t", "new/test")))
	case "connections":
		bindings = append(bindings, key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new connection")))
	case "discovered":
		bindings = append(bindings, key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "create Physical")))
	case "physical", "combos":
		bindings = append(bindings, key.NewBinding(key.WithKeys("c/p"), key.WithHelp("c/p", "compose/expose")))
	}
	bindings = append(bindings,
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keybindings")),
	)
	return bindings
}

func (h keyHelp) ShortHelp() []key.Binding { return h.shortHelp() }

func (keyHelp) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{key.NewBinding(key.WithKeys("h/l"), key.WithHelp("h/l", "previous/next block")), key.NewBinding(key.WithKeys("1-5"), key.WithHelp("1-5", "focus block"))},
		{key.NewBinding(key.WithKeys("[/]"), key.WithHelp("[/]", "previous/next tab")), key.NewBinding(key.WithKeys("0"), key.WithHelp("0", "focus main view"))},
		{key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "move inside pane")), key.NewBinding(key.WithKeys("gg/G"), key.WithHelp("gg/G", "first/last item"))},
		{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "focus inspector")), key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "select model"))},
		{key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "fuzzy search")), key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh pane"))},
		{key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "assign discovered routes to Physical")), key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "compose selected models"))},
		{key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "toggle model exposure")), key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "test provider + discover"))},
	}
}

type app struct {
	ipcPath string
	width   int
	height  int

	panels      []dashboardPanel
	activePanel int
	activeTabs  []int
	screenMode  int
	focus       focusPane
	mode        appMode
	dashboard   viewport.Model
	detail      viewport.Model
	help        help.Model
	picker      list.Model
	form        *formState
	command     textinput.Model
	commandErr  string

	providers         []providerNode
	providerContextID string
	raw               map[sectionID]json.RawMessage
	pickerSelected    map[string]bool
	sourceItems       []entry

	status        string
	lastError     string
	loading       bool
	confirmTitle  string
	confirmMethod string
	confirmParams map[string]any
	confirmAction string
	lastG         bool
}

func newApp(ipcPath string) *app {
	var panels []dashboardPanel
	for _, block := range dashboardPanes {
		for _, definition := range block.tabs {
			selected := map[string]bool{}
			rows := list.New([]list.Item{}, itemDelegate{selected: selected}, 40, 2)
			rows.SetShowTitle(false)
			rows.SetShowFilter(false)
			rows.SetShowStatusBar(false)
			rows.SetShowPagination(false)
			rows.SetShowHelp(false)
			rows.DisableQuitKeybindings()
			rows.InfiniteScrolling = false
			rows.Styles = list.DefaultStyles(true)
			rows.Styles.NoItems = muted
			panels = append(panels, dashboardPanel{definition: definition, list: rows, loading: len(definition.sources) > 0, selected: selected})
		}
	}
	pickerSelected := map[string]bool{}
	picker := list.New([]list.Item{}, itemDelegate{selected: pickerSelected}, 60, 12)
	picker.SetShowTitle(false)
	picker.SetShowFilter(false)
	picker.SetShowStatusBar(false)
	picker.SetShowPagination(false)
	picker.SetShowHelp(false)
	picker.DisableQuitKeybindings()
	picker.Styles = list.DefaultStyles(true)
	command := textinput.New()
	command.Prompt = ":"
	command.CharLimit = 160
	command.Blur()
	activeTabs := make([]int, len(dashboardPanes))
	activeTabs[dashboardConnections] = 1
	model := &app{
		ipcPath:        ipcPath,
		panels:         panels,
		activePanel:    int(dashboardConnections),
		activeTabs:     activeTabs,
		focus:          focusDashboard,
		dashboard:      viewport.New(),
		detail:         viewport.New(),
		help:           help.New(),
		picker:         picker,
		command:        command,
		raw:            map[sectionID]json.RawMessage{},
		pickerSelected: pickerSelected,
		status:         "connecting to daemon…",
		loading:        true,
	}
	model.setPaneItems(model.contextIndex(3, 0), []entry{{key: "usage-unavailable", title: "Usage API unavailable", summary: "daemon has no usage read endpoint", detail: "Usage data is not available through the current daemon IPC contract."}})
	model.setPaneItems(model.contextIndex(4, 1), []entry{{key: "logs-unavailable", title: "Log streaming unavailable", summary: "daemon has no log read endpoint", detail: "The TUI does not read journalctl directly. The daemon IPC contract currently has no log streaming endpoint."}})
	return model
}

func (m *app) Init() tea.Cmd {
	commands := make([]tea.Cmd, 0, len(sections))
	for _, item := range sections {
		commands = append(commands, m.fetch(item.id))
	}
	return tea.Batch(commands...)
}

func (m *app) fetch(id sectionID) tea.Cmd {
	item := sections[id]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, m.ipcPath, daemon.IPCRequest{ID: "gobroom", Method: item.method})
		if err != nil {
			return fetchMsg{section: id, err: err}
		}
		result, err := json.Marshal(response.Result)
		return fetchMsg{section: id, result: result, err: err}
	}
}

func (m *app) invoke(method string, params map[string]any) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, m.ipcPath, daemon.IPCRequest{ID: "gobroom", Method: method, Params: params})
		if err != nil {
			return actionMsg{method: method, err: err}
		}
		result, err := json.Marshal(response.Result)
		return actionMsg{method: method, result: result, err: err}
	}
}

func (m *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.resize()
		return m, nil
	case fetchMsg:
		indices := m.panelsForResource(message.section)
		if message.err != nil {
			for _, index := range indices {
				m.panels[index].loading = false
				m.panels[index].err = message.err.Error()
			}
			m.updateLoading()
			if containsIndex(indices, m.activeContextIndex()) {
				m.lastError = message.err.Error()
				m.status = "request failed: " + sections[message.section].label
				m.syncDetail()
			}
			return m, nil
		}
		m.raw[message.section] = message.result
		if message.section == sectionProviders {
			var values []providerNode
			if err := json.Unmarshal(message.result, &values); err == nil {
				m.providers = values
			}
		}
		if (message.section == sectionModels || message.section == sectionProviders) && len(m.raw[sectionProviders]) > 0 && len(m.raw[sectionModels]) > 0 {
			m.buildPickerSources()
		}
		var cmds []tea.Cmd
		for index := range m.panels {
			if m.panelReady(index) && containsIndex(indices, index) {
				cmds = append(cmds, m.rebuildDashboardPane(index))
			}
		}
		m.updateLoading()
		if containsIndex(indices, m.activeContextIndex()) {
			m.lastError = ""
			m.status = fmt.Sprintf("%d models/items · %s", len(m.active().items), time.Now().Format("15:04:05"))
			m.syncDetail()
		}
		return m, tea.Batch(cmds...)
	case actionMsg:
		m.loading = false
		if message.err != nil {
			m.lastError = message.err.Error()
			m.status = "action failed"
			m.mode = modeBrowse
			m.form = nil
			m.syncDetail()
			return m, nil
		}
		m.mode = modeBrowse
		m.form = nil
		m.lastError = ""
		m.status = message.method + " completed"
		if message.method == "providers.refresh_models" {
			var result struct {
				Models int
				URL    string
			}
			if json.Unmarshal(message.result, &result) == nil {
				m.status = fmt.Sprintf("test passed · imported %d source models from %s", result.Models, result.URL)
			}
		}
		return m, m.refreshAfter(message.method)
	case tea.KeyPressMsg:
		return m.updateKey(message)
	}

	if m.mode == modeForm && m.form != nil {
		updated, cmd, _, _, _ := m.form.Update(msg)
		m.form = updated
		return m, cmd
	}
	if m.focus == focusInspector {
		updated, cmd := m.detail.Update(msg)
		m.detail = updated
		return m, cmd
	}
	return m, nil
}

func (m *app) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyText := msg.String()
	if m.mode == modeForm {
		if m.form == nil {
			m.mode = modeBrowse
			return m, nil
		}
		updated, cmd, save, params, resultErr := m.form.Update(msg)
		m.form = updated
		if m.form == nil {
			m.mode = modeBrowse
			m.syncDetail()
			return m, cmd
		}
		if resultErr != nil {
			m.status = resultErr.Error()
			return m, nil
		}
		if save {
			m.mode = modeBrowse
			m.loading = true
			m.status = "saving…"
			return m, m.invoke(m.form.method, params)
		}
		return m, cmd
	}
	if m.mode == modeSourcePicker {
		return m.updateSourcePickerKey(msg)
	}
	if m.mode == modeCommand {
		if keyText == "esc" {
			m.mode = modeBrowse
			m.command.Blur()
			m.command.Reset()
			m.commandErr = ""
			return m, nil
		}
		if keyText == "enter" {
			return m, m.runCommand()
		}
		input, cmd := m.command.Update(msg)
		m.command = input
		return m, cmd
	}
	if m.mode == modeConfirm {
		switch keyText {
		case "y", "Y", "enter":
			m.mode = modeBrowse
			m.loading = true
			m.status = m.confirmAction + "…"
			return m, m.invoke(m.confirmMethod, m.confirmParams)
		case "n", "N", "esc", "q":
			m.mode = modeBrowse
			m.status = "cancelled"
			return m, nil
		}
		return m, nil
	}
	if m.mode == modeHelp {
		if keyText == "?" || keyText == "esc" || keyText == "q" {
			m.mode = modeBrowse
		}
		return m, nil
	}
	current := m.active()
	if current.list.FilterState() == list.Filtering {
		updated, cmd := current.list.Update(msg)
		current.list = updated
		m.syncDetail()
		return m, cmd
	}
	if keyText == "ctrl+c" || keyText == "q" {
		return m, tea.Quit
	}
	if keyText == "?" {
		m.mode = modeHelp
		return m, nil
	}
	if keyText == "0" {
		m.focus = focusInspector
		m.syncDetail()
		return m, nil
	}
	if keyText == "+" || keyText == "_" {
		delta := 1
		if keyText == "_" {
			delta = -1
		}
		m.screenMode = (m.screenMode + delta + 3) % 3
		return m, nil
	}
	if keyText == ":" {
		m.mode = modeCommand
		m.command.Reset()
		m.command.Focus()
		m.commandErr = ""
		return m, nil
	}
	if keyText == "esc" {
		if m.focus == focusInspector {
			m.focus = focusDashboard
			m.syncDetail()
			return m, nil
		}
		if m.selectedCount() > 0 {
			clear(m.active().selected)
			m.status = "selection cleared"
			return m, nil
		}
	}
	if index := dashboardPaneForKey(keyText); index >= 0 {
		m.focusPanel(index)
		return m, nil
	}
	if keyText == "[" || keyText == "]" {
		if m.focus == focusDashboard {
			delta := -1
			if keyText == "]" {
				delta = 1
			}
			m.changeTab(delta)
		}
		return m, nil
	}
	if keyText == "n" || keyText == "N" {
		if m.focus == focusDashboard && current.list.FilterState() != list.Unfiltered {
			filtered := current.list.VisibleItems()
			if len(filtered) > 0 {
				delta := 1
				if keyText == "N" {
					delta = -1
				}
				current.list.Select((current.list.Index() + delta + len(filtered)) % len(filtered))
				m.syncDetail()
			}
			return m, nil
		}
	}
	if keyText == "h" || keyText == "shift+tab" {
		if m.focus == focusDashboard {
			m.focusPanel((m.activePanel - 1 + len(dashboardPanes)) % len(dashboardPanes))
		}
		return m, nil
	}
	if keyText == "l" || keyText == "tab" {
		if m.focus == focusDashboard {
			m.focusPanel((m.activePanel + 1) % len(dashboardPanes))
		}
		return m, nil
	}
	if m.focus == focusInspector {
		switch keyText {
		case "e", "p", "d", "t", "c", "f", "a", "r":
			m.focus = focusDashboard
			model, cmd := m.updateDashboardAction(msg)
			if m.mode != modeBrowse {
				m.focus = focusInspector
			} else if m.focus == focusDashboard {
				m.focus = focusInspector
			}
			return model, cmd
		}
		if keyText == "H" {
			m.detail.ScrollLeft(6)
			return m, nil
		}
		if keyText == "L" {
			m.detail.ScrollRight(6)
			return m, nil
		}
		updated, cmd := m.detail.Update(msg)
		m.detail = updated
		return m, cmd
	}
	if msg.Key().Code == tea.KeySpace {
		if selected := m.selectedEntry(); selected != nil {
			panel := m.active()
			panel.selected[selected.key] = !panel.selected[selected.key]
			if !panel.selected[selected.key] {
				delete(panel.selected, selected.key)
			}
			m.status = fmt.Sprintf("%d selected", m.selectedCount())
		}
		return m, nil
	}
	return m.updateDashboardAction(msg)
}

func (m *app) updateDashboardAction(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyText := msg.String()
	current := m.active()
	switch keyText {
	case "enter":
		if selected := m.selectedEntry(); selected != nil {
			switch selected.payload.(type) {
			case physicalModel, comboModel:
				m.form = newResourceForm(0, selected, "")
				if m.form != nil {
					m.mode = modeForm
					m.focus = focusInspector
					m.resize()
					return m, nil
				}
			}
		}
		m.focus = focusInspector
		m.syncDetail()
		return m, nil
	case "r":
		return m, m.refreshPane(m.activePanel)
	case "n":
		m.form = m.newModelForm()
		if m.form == nil {
			m.status = "no create action in this pane"
			return m, nil
		}
		m.mode = modeForm
		m.resize()
		return m, nil
	case "e":
		selected := m.selectedEntry()
		if selected == nil {
			m.status = "focus a row first"
			return m, nil
		}
		switch selected.payload.(type) {
		case physicalModel, comboModel:
			m.form = newResourceForm(0, selected, "")
		default:
			m.status = "this row is not an editable typed model"
			return m, nil
		}
		if m.form == nil {
			m.status = "this model definition is read-only"
			return m, nil
		}
		m.mode = modeForm
		m.resize()
		return m, nil
	case "d":
		return m, m.confirmDelete()
	case "t":
		if current.definition.id == dashboardConnections {
			if selected := m.selectedEntry(); selected != nil && selected.kind == "provider" {
				return m, m.testAndDiscover()
			}
		}
	case "a":
		if current.definition.id == dashboardConnections {
			if selected := m.selectedEntry(); selected != nil {
				if node, ok := selected.payload.(providerNode); ok {
					m.form = newResourceForm(int(sectionModels), nil, node.ID)
					m.mode = modeForm
					m.resize()
				}
			}
		} else if current.definition.view == "discovered" {
			if m.providerContextID == "" {
				m.status = "choose a provider in Providers, then return to add a custom upstream model"
				return m, nil
			}
			m.form = newResourceForm(int(sectionModels), nil, m.providerContextID)
			m.mode = modeForm
			m.resize()
		} else if current.definition.view == "physical" {
			m.status = "select routes in the Discovered tab and press f"
			return m, nil
		}
	case "c":
		if current.definition.id == dashboardConnections {
			if selected := m.selectedEntry(); selected != nil {
				if node, ok := selected.payload.(providerNode); ok {
					m.form = newResourceForm(int(sectionConnections), nil, node.ID)
					m.mode = modeForm
					m.resize()
				} else {
					m.status = "focus a provider row to add a connection"
				}
			}
		} else if current.definition.id == dashboardModels && (current.definition.view == "physical" || current.definition.view == "combos") {
			members := m.selectedTypedModelRefs()
			m.form = newComboModelForm(members)
			m.form.title = "Create combo from selected models"
			m.mode = modeForm
			m.resize()
		}
	case "f":
		if current.definition.id == dashboardModels && current.definition.view == "discovered" {
			sources := m.selectedRouteRefs()
			m.form = newPhysicalModelForm(sources)
			m.mode = modeForm
			m.resize()
			return m, nil
		}
	case "p":
		if current.definition.id == dashboardModels && current.definition.view != "discovered" {
			return m, m.toggleExposure()
		}
	case "g":
		if m.lastG {
			current.list.GoToStart()
			m.lastG = false
			m.syncDetail()
			return m, nil
		}
		m.lastG = true
		return m, nil
	case "G":
		m.lastG = false
		current.list.GoToEnd()
		m.syncDetail()
		return m, nil
	}
	if m.lastG {
		m.lastG = false
	}
	updated, cmd := current.list.Update(msg)
	current.list = updated
	m.syncDetail()
	m.captureProviderContext()
	return m, cmd
}

func dashboardPaneForKey(value string) int {
	for index, pane := range dashboardPanes {
		if value == pane.key {
			return index
		}
	}
	return -1
}

func (m *app) focusPanel(index int) {
	if index < 0 || index >= len(dashboardPanes) {
		return
	}
	m.activePanel = index
	m.focus = focusDashboard
	panel := m.active()
	m.loading = panel.loading
	m.lastError = panel.err
	if m.loading {
		m.status = "loading " + panel.definition.label + "…"
	} else {
		m.status = fmt.Sprintf("%d %s", len(panel.items), strings.ToLower(panel.definition.label))
	}
	m.syncDetail()
}

func (m *app) contextIndex(block, tab int) int {
	if block < 0 || block >= len(dashboardPanes) {
		return 0
	}
	for index := 0; index < block; index++ {
		tab += len(dashboardPanes[index].tabs)
	}
	return tab
}

func (m *app) activeContextIndex() int {
	if m.activePanel < 0 || m.activePanel >= len(m.activeTabs) {
		return 0
	}
	return m.contextIndex(m.activePanel, m.activeTabs[m.activePanel])
}

func (m *app) active() *dashboardPanel { return &m.panels[m.activeContextIndex()] }

func (m *app) changeTab(delta int) {
	if len(dashboardPanes[m.activePanel].tabs) < 2 {
		return
	}
	next := m.activeTabs[m.activePanel] + delta
	if next < 0 {
		next = len(dashboardPanes[m.activePanel].tabs) - 1
	}
	if next >= len(dashboardPanes[m.activePanel].tabs) {
		next = 0
	}
	m.activeTabs[m.activePanel] = next
	panel := m.active()
	m.loading, m.lastError = panel.loading, panel.err
	m.status = fmt.Sprintf("%s · %d items", panel.definition.label, len(panel.items))
	m.syncDetail()
}

func (m *app) selectedEntry() *entry {
	selected, ok := m.active().list.SelectedItem().(entry)
	if !ok {
		return nil
	}
	return &selected
}

func (m *app) selectedCount() int { return len(m.active().selected) }

func (m *app) selectedModelRefs() []string {
	selected := m.active().selected
	refs := make([]string, 0, len(selected))
	for _, item := range m.active().items {
		if selected[item.key] && item.modelRef != "" {
			refs = append(refs, item.modelRef)
		}
	}
	return refs
}

func (m *app) selectedRouteRefs() []routeReference {
	panel := m.active()
	result := make([]routeReference, 0, len(panel.selected))
	for _, item := range panel.items {
		if panel.selected[item.key] {
			if route, ok := item.payload.(discoveredRoute); ok {
				result = append(result, routeReference{RouteID: route.ID})
			}
		}
	}
	if len(result) == 0 {
		if selected := m.selectedEntry(); selected != nil {
			if route, ok := selected.payload.(discoveredRoute); ok {
				result = append(result, routeReference{RouteID: route.ID})
			}
		}
	}
	return result
}

func (m *app) selectedTypedModelRefs() []modelReference {
	panel := m.active()
	result := make([]modelReference, 0, len(panel.selected))
	appendEntry := func(item entry) {
		switch item.payload.(type) {
		case physicalModel:
			result = append(result, modelReference{Kind: "physical", ID: item.modelRef})
		case comboModel:
			result = append(result, modelReference{Kind: "combo", ID: item.modelRef})
		}
	}
	for _, item := range panel.items {
		if panel.selected[item.key] {
			appendEntry(item)
		}
	}
	if len(result) == 0 {
		if selected := m.selectedEntry(); selected != nil {
			appendEntry(*selected)
		}
	}
	return result
}

func (m *app) panelsForResource(id sectionID) []int {
	result := make([]int, 0, 2)
	for index, pane := range m.panels {
		for _, source := range pane.definition.sources {
			if source == id {
				result = append(result, index)
				break
			}
		}
	}
	return result
}

func containsIndex(indices []int, wanted int) bool {
	for _, index := range indices {
		if index == wanted {
			return true
		}
	}
	return false
}

func (m *app) panelReady(index int) bool {
	for _, source := range m.panels[index].definition.sources {
		if m.panels[index].err != "" || len(m.raw[source]) == 0 {
			return false
		}
	}
	return true
}

func (m *app) updateLoading() {
	m.loading = false
	for index := range m.panels {
		if m.panels[index].loading {
			m.loading = true
			return
		}
	}
}

func (m *app) rebuildDashboardPane(index int) tea.Cmd {
	var items []entry
	var err error
	definition := m.panels[index].definition
	switch definition.view {
	case "status":
		items, err = makeEntries("status", m.raw[sectionStatus], m.providers)
	case "providers":
		items, err = makeEntries("providers.list", m.raw[sectionProviders], m.providers)
		for index := range items {
			items[index].kind = "provider"
		}
	case "connections":
		items, err = makeEntries("connections.list", m.raw[sectionConnections], m.providers)
		for index := range items {
			items[index].kind = "connection"
		}
	case "discovered":
		items, err = makeDiscoveredEntries(m.raw[sectionDiscovered], m.providers)
	case "physical", "combos":
		workspace, buildErr := (modelWorkspaceClient{providers: m.providers}).Build(m.raw[sectionDiscovered], m.raw[sectionPhysical], m.raw[sectionComboModels])
		err = buildErr
		if definition.view == "physical" {
			items = workspace.Physical
		} else {
			items = workspace.Combos
		}
	case "quota":
		items, err = makeEntries("quota.list", m.raw[sectionQuota], m.providers)
	case "usage":
		items, err = makeEntries("usage.list", m.raw[sectionUsage], m.providers)
	case "health":
		items, err = makeEntries("health.list", m.raw[sectionHealth], m.providers)
	case "unavailable-usage":
		items = []entry{{key: "usage-unavailable", title: "Usage API unavailable", summary: "daemon has no usage read endpoint", detail: "Usage data is not available through the current daemon IPC contract."}}
	case "unavailable-logs":
		items = []entry{{key: "logs-unavailable", title: "Log streaming unavailable", summary: "daemon has no log read endpoint", detail: "The TUI does not read journalctl directly. The daemon IPC contract currently has no log streaming endpoint."}}
	}
	if err != nil {
		m.panels[index].err = err.Error()
		m.panels[index].loading = false
		return nil
	}
	m.panels[index].err = ""
	m.panels[index].loading = false
	return m.setPaneItems(index, items)
}

func (m *app) setPaneItems(index int, items []entry) tea.Cmd {
	panel := &m.panels[index]
	selectedKey := ""
	if selected, ok := panel.list.SelectedItem().(entry); ok {
		selectedKey = selected.key
	}
	converted := make([]list.Item, 0, len(items))
	for _, item := range items {
		converted = append(converted, item)
	}
	panel.items = items
	cmd := panel.list.SetItems(converted)
	if selectedKey != "" {
		for row, item := range items {
			if item.key == selectedKey {
				panel.list.Select(row)
				break
			}
		}
	}
	m.resize()
	if index == m.activeContextIndex() {
		m.syncDetail()
	}
	return cmd
}

func (m *app) setPickerItems(items []entry) {
	m.sourceItems = items
	converted := make([]list.Item, 0, len(items))
	for _, item := range items {
		converted = append(converted, item)
	}
	_ = m.picker.SetItems(converted)
	if m.width > 0 {
		m.picker.SetSize(max(20, m.width*60/100), max(4, m.height-12))
	}
}

func (m *app) buildPickerSources() {
	if len(m.raw[sectionModels]) == 0 || len(m.raw[sectionProviders]) == 0 {
		return
	}
	items, err := makeSourceEntries(m.raw[sectionModels], m.providers)
	if err == nil {
		m.setPickerItems(items)
	}
}

func (m *app) captureProviderContext() {
	if m.active().definition.id != dashboardConnections {
		return
	}
	if selected := m.selectedEntry(); selected != nil {
		if node, ok := selected.payload.(providerNode); ok {
			m.providerContextID = node.ID
		}
	}
}

func (m *app) syncDetail() {
	if m.mode == modeForm || m.mode == modeConfirm || m.mode == modeHelp || m.mode == modeSourcePicker {
		return
	}
	selected := m.selectedEntry()
	if selected != nil {
		m.detail.SetContent(m.mainPreview(*selected))
		m.captureProviderContext()
		return
	}
	panel := m.active()
	if panel.loading {
		m.detail.SetContent("Loading " + panel.definition.label + "…")
	} else if panel.err != "" {
		m.detail.SetContent("Request failed\n\n" + panel.err)
	} else {
		m.detail.SetContent("This pane is empty.")
	}
}

func (m *app) mainPreview(selected entry) string {
	actions := []string{"Enter / 0  focus Main", "+ / _      resize context"}
	switch selected.payload.(type) {
	case providerNode:
		actions = append(actions, "e            edit provider", "t            test and discover", "c            add connection")
	case connection:
		actions = append(actions, "e            edit connection", "d            delete connection")
	case discoveredRoute:
		actions = append(actions, "Space        select route", "f            build Physical from selection")
	case physicalModel:
		actions = append(actions, "e            edit sources and policy", "p            toggle /v1/models exposure", "c            compose into Combo", "d            delete Physical")
	case comboModel:
		actions = append(actions, "e            edit members and strategy", "p            toggle /v1/models exposure", "c            compose nested Combo", "d            delete Combo")
	}
	return lipgloss.JoinVertical(lipgloss.Left, selected.detail, "", muted.Render("Actions"), muted.Render(strings.Join(actions, "\n")))
}

func (m *app) resize() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	bodyHeight := max(5, m.height-7)
	leftWidth := max(34, m.width*36/100)
	rightWidth := max(24, m.width-leftWidth)
	if m.width < 90 {
		leftWidth, rightWidth = m.width, m.width
	}
	m.dashboard.SetWidth(max(1, leftWidth))
	m.dashboard.SetHeight(bodyHeight)
	m.detail.SetWidth(max(1, rightWidth-4))
	m.detail.SetHeight(bodyHeight - 2)
	m.detail.SoftWrap = true
	m.help.SetWidth(m.width)
	for index := range m.panels {
		m.panels[index].list.SetSize(max(1, leftWidth-6), 2)
	}
	m.picker.SetSize(max(20, rightWidth-6), max(4, bodyHeight-6))
	if m.form != nil {
		m.form.SetWidth(max(12, rightWidth-11))
	}
	m.command.SetWidth(max(12, m.width-4))
}

func (m *app) openSourcePicker() tea.Cmd {
	if len(m.sourceItems) == 0 {
		m.status = "load provider models first with t in Connections"
		return nil
	}
	clear(m.pickerSelected)
	m.mode = modeSourcePicker
	m.picker.GoToStart()
	return nil
}

func (m *app) updateSourcePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyText := msg.String()
	if m.picker.FilterState() == list.Filtering {
		updated, cmd := m.picker.Update(msg)
		m.picker = updated
		return m, cmd
	}
	switch keyText {
	case "esc", "q":
		m.mode = modeBrowse
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		refs := make([]string, 0, len(m.pickerSelected))
		for _, source := range m.sourceItems {
			if m.pickerSelected[source.key] && source.routeRef != "" {
				refs = append(refs, source.routeRef)
			}
		}
		if len(refs) == 0 {
			m.status = "select one or more provider variants with Space"
			return m, nil
		}
		m.form = newResourceForm(int(sectionPhysical), nil, "")
		m.form.title = "Create physical model"
		m.form.comboMembers = refs
		m.form.memberCursor = len(refs) - 1
		m.mode = modeForm
		m.resize()
		return m, nil
	}
	if msg.Key().Code == tea.KeySpace {
		if source, ok := m.picker.SelectedItem().(entry); ok {
			m.pickerSelected[source.key] = !m.pickerSelected[source.key]
			if !m.pickerSelected[source.key] {
				delete(m.pickerSelected, source.key)
			}
		}
		return m, nil
	}
	updated, cmd := m.picker.Update(msg)
	m.picker = updated
	return m, cmd
}

func (m *app) toggleExposure() tea.Cmd {
	selected := m.selectedEntry()
	if selected == nil || selected.modelRef == "" {
		m.status = "focus a model first"
		return nil
	}
	switch value := selected.payload.(type) {
	case physicalModel:
		value.Discoverable = !value.Discoverable
		m.loading = true
		m.status = "updating physical model exposure…"
		return m.invoke("physical_models.upsert", map[string]any{"name": value.Name, "identity": value.Identity, "sources": value.Sources, "policy": value.Policy, "profile": value.Profile, "limits": value.Limits, "discoverable": value.Discoverable, "enabled": value.Enabled})
	case comboModel:
		value.Discoverable = !value.Discoverable
		m.loading = true
		m.status = "updating combo model exposure…"
		return m.invoke("combo_models.upsert", map[string]any{"name": value.Name, "members": value.Members, "strategy": value.Strategy, "discoverable": value.Discoverable, "enabled": value.Enabled})
	}
	m.status = "focus a typed Physical or Combo model to toggle discoverable"
	return nil
}

func (m *app) testAndDiscover() tea.Cmd {
	selected := m.selectedEntry()
	if selected == nil {
		m.status = "focus a provider first"
		return nil
	}
	node, ok := selected.payload.(providerNode)
	if !ok {
		m.status = "focus a provider row first"
		return nil
	}
	m.loading = true
	m.status = "testing endpoint and importing model sources…"
	return m.invoke("providers.refresh_models", map[string]any{"nodeID": node.ID})
}

func (m *app) confirmDelete() tea.Cmd {
	selected := m.selectedEntry()
	if selected == nil {
		m.status = "focus a row first"
		return nil
	}
	var method string
	params := map[string]any{}
	switch value := selected.payload.(type) {
	case providerNode:
		method, params = "providers.delete", map[string]any{"id": value.ID}
	case connection:
		method, params = "connections.delete", map[string]any{"id": value.ID}
	case physicalModel:
		method, params = "physical_models.delete", map[string]any{"name": value.Name}
	case comboModel:
		method, params = "combo_models.delete", map[string]any{"name": value.Name}
	default:
		m.status = "source catalog rows are managed through their provider"
		return nil
	}
	m.confirmMethod, m.confirmParams = method, params
	m.confirmTitle = "Delete “" + selected.title + "”?"
	m.confirmAction = "deleting"
	m.mode = modeConfirm
	return nil
}

func (m *app) refreshPane(index int) tea.Cmd {
	if index < 0 || index >= len(dashboardPanes) {
		return nil
	}
	panel := m.active()
	panel.loading = true
	panel.err = ""
	m.loading = true
	m.status = "refreshing " + panel.definition.label + "…"
	commands := make([]tea.Cmd, 0, len(panel.definition.sources))
	for _, resource := range panel.definition.sources {
		commands = append(commands, m.fetch(resource))
	}
	return tea.Batch(commands...)
}

func (m *app) refreshAfter(method string) tea.Cmd {
	resources := []sectionID{}
	switch method {
	case "providers.refresh_models":
		resources = []sectionID{sectionProviders, sectionModels}
	case "providers.create", "providers.update", "providers.delete":
		resources = []sectionID{sectionProviders, sectionConnections, sectionModels}
	case "connections.create", "connections.update", "connections.delete":
		resources = []sectionID{sectionConnections}
	case "custom_models.upsert", "custom_models.delete":
		resources = []sectionID{sectionModels}
	case "physical_models.upsert", "physical_models.delete":
		resources = []sectionID{sectionPhysical}
	case "combo_models.upsert", "combo_models.delete":
		resources = []sectionID{sectionComboModels}
	default:
		return m.refreshPane(m.activePanel)
	}
	commands := make([]tea.Cmd, 0, len(resources))
	for _, resource := range resources {
		for _, index := range m.panelsForResource(resource) {
			m.panels[index].loading = true
		}
		commands = append(commands, m.fetch(resource))
	}
	m.loading = true
	return tea.Batch(commands...)
}

func (m *app) runCommand() tea.Cmd {
	command := strings.TrimSpace(m.command.Value())
	m.command.Blur()
	m.command.Reset()
	m.mode = modeBrowse
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "q", "quit":
		return tea.Quit
	case "r", "refresh":
		return m.refreshPane(m.activePanel)
	case "new":
		m.form = m.newModelForm()
		if m.form == nil {
			m.status = "no create action in this pane"
		} else {
			m.mode = modeForm
			m.resize()
		}
	case "edit":
		return m.openEdit()
	case "delete":
		return m.confirmDelete()
	case "help":
		m.mode = modeHelp
	default:
		m.commandErr = "unknown command: " + fields[0]
		m.status = m.commandErr
	}
	return nil
}

func (m *app) openEdit() tea.Cmd {
	selected := m.selectedEntry()
	if selected == nil {
		m.status = "focus a row first"
		return nil
	}
	if _, ok := selected.payload.(physicalModel); !ok {
		if _, ok := selected.payload.(comboModel); !ok {
			m.status = "focus a typed model first"
			return nil
		}
	}
	m.form = newResourceForm(0, selected, "")
	if m.form == nil {
		m.status = "this model/source is read-only"
	} else {
		m.mode = modeForm
		m.resize()
	}
	return nil
}

func (m *app) newModelForm() *formState {
	switch m.active().definition.id {
	case dashboardConnections:
		return newResourceForm(int(sectionProviders), nil, "")
	case dashboardModels:
		switch m.active().definition.view {
		case "physical":
			return newPhysicalModelForm(nil)
		case "combos":
			return newComboModelForm(nil)
		default:
			return nil
		}
	default:
		return nil
	}
}

func (m *app) View() tea.View {
	var view tea.View
	view.AltScreen = true
	view.WindowTitle = "GoBroom"
	if m.width <= 0 || m.height <= 0 {
		view.SetContent("Starting GoBroom…")
		return view
	}
	view.SetContent(m.render())
	return view
}

func (m *app) render() string {
	width := max(1, m.width)
	header := lipgloss.NewStyle().Bold(true).Render("GOBROOM") + "  " + muted.Render("daemon control") + strings.Repeat(" ", max(1, width-43)) + muted.Render("v"+version)
	contextLine := muted.Render("DASHBOARD  /  " + strings.ToUpper(m.active().definition.label))
	if m.loading {
		contextLine += muted.Render("   ·   loading…")
	}
	header = lipgloss.JoinVertical(lipgloss.Left, header, contextLine)
	bodyHeight := max(5, m.height-7)
	var body string
	if m.screenMode == 2 {
		if m.focus == focusInspector {
			body = panel("[0] Main · "+m.active().definition.label, m.renderMainView(), width, bodyHeight, true)
		} else {
			body = m.renderActiveBlock(width, bodyHeight)
		}
	} else if m.screenMode == 1 && width >= 70 {
		leftWidth := max(30, width/2)
		rightWidth := max(24, width-leftWidth)
		left := m.renderActiveBlock(leftWidth, bodyHeight)
		right := panel("[0] Main · "+m.active().definition.label, m.renderMainView(), rightWidth, bodyHeight, m.focus == focusInspector)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	} else if width < 90 {
		if m.focus == focusDashboard {
			body = m.renderDashboard(width, bodyHeight)
		} else {
			body = panel("[0] Main · "+m.active().definition.label, m.renderMainView(), width, bodyHeight, true)
		}
	} else {
		leftWidth := max(34, width*36/100)
		rightWidth := max(24, width-leftWidth)
		left := m.renderDashboard(leftWidth, bodyHeight)
		right := panel("[0] Main · "+m.active().definition.label, m.renderMainView(), rightWidth, bodyHeight, m.focus == focusInspector)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, m.renderFooter(width))
}

func (m *app) renderActiveBlock(width, height int) string {
	block := dashboardPanes[m.activePanel]
	context := &m.panels[m.activeContextIndex()]
	context.list.SetSize(max(1, width-6), max(1, height-3))
	return panel(m.blockTitle(m.activePanel, block, context), m.panelContent(context, width), width, height, m.focus == focusDashboard)
}

func (m *app) blockTitle(index int, block dashboardBlockDef, context *dashboardPanel) string {
	title := fmt.Sprintf("[%s] %s", block.key, block.label)
	if len(block.tabs) > 1 {
		tabs := make([]string, len(block.tabs))
		for tabIndex, tab := range block.tabs {
			if tabIndex == m.activeTabs[index] {
				tabs[tabIndex] = "[" + tab.label + "]"
			} else {
				tabs[tabIndex] = tab.label
			}
		}
		title += " " + strings.Join(tabs, "/")
	} else {
		title += fmt.Sprintf(" · %d", len(context.items))
	}
	if context.list.FilterState() != list.Unfiltered {
		title += "  /" + context.list.FilterInput.Value()
	}
	return title
}

func (m *app) panelContent(item *dashboardPanel, width int) string {
	switch {
	case item.err != "":
		return errorStyle.Render(ansi.Truncate(item.err, max(8, width-8), "…"))
	case item.loading && len(item.items) == 0:
		return muted.Render("Loading…")
	case len(item.items) == 0:
		return muted.Render("(empty)")
	default:
		return item.list.View()
	}
}

func (m *app) renderDashboard(width, height int) string {
	heights := m.dashboardFrameHeights(height)
	frames := make([]string, 0, len(dashboardPanes))
	for index, block := range dashboardPanes {
		frameHeight := heights[index]
		item := &m.panels[m.contextIndex(index, m.activeTabs[index])]
		item.list.SetSize(max(1, width-6), frameHeight-3)
		title := m.blockTitle(index, block, item)
		content := m.panelContent(item, width)
		frames = append(frames, panel(title, content, width, frameHeight, m.focus == focusDashboard && index == m.activePanel))
	}
	m.dashboard.SetContent(strings.Join(frames, "\n"))
	m.dashboard.SetWidth(width)
	m.dashboard.SetHeight(height)
	activeTop := 0
	for index := 0; index < m.activePanel; index++ {
		activeTop += heights[index] + 1
	}
	activeHeight := heights[m.activePanel]
	offset := m.dashboard.YOffset()
	if activeTop < offset {
		offset = activeTop
	} else if activeTop+activeHeight > offset+height {
		offset = activeTop + activeHeight - height
	}
	m.dashboard.SetYOffset(offset)
	return m.dashboard.View()
}

func (m *app) dashboardFrameHeights(total int) []int {
	const minimum = 3
	heights := make([]int, len(dashboardPanes))
	for index := range heights {
		heights[index] = minimum
	}
	available := total - (len(heights) - 1) - minimum*len(heights)
	if available <= 0 {
		return heights
	}
	models := int(dashboardModels)
	if m.activePanel == models {
		heights[models] += available
		return heights
	}
	activeExtra := min(6, available)
	if m.activePanel == int(dashboardStatus) {
		activeExtra = min(3, available)
	}
	heights[m.activePanel] += activeExtra
	heights[models] += available - activeExtra
	return heights
}

func (m *app) renderMainView() string {
	switch m.mode {
	case modeForm:
		return m.renderForm()
	case modeSourcePicker:
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("Choose provider model variants"),
			muted.Render("Search with / · Space selects · Enter continues to the canonical model name"),
			m.picker.View(),
			muted.Render(fmt.Sprintf("%d source variants selected", len(m.pickerSelected))),
		)
	case modeConfirm:
		return lipgloss.JoinVertical(lipgloss.Left, errorStyle.Bold(true).Render(m.confirmTitle), "", "This change is applied to the daemon immediately.", "", keyLine("y / enter", "confirm"), keyLine("n / esc", "cancel"))
	case modeHelp:
		return lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Bold(true).Render("Keyboard map"), "", "h / l  previous / next block", "j / k  move inside active context", "[ / ]  previous / next tab", "enter  open selected context in main view", "esc    return to parent view", "space  select item", "1–5    focus a block · 0 main view", "/      filter this context · n/N next match", "f      create Physical from discovered routes", "c      compose selected Physical models", "p      toggle exposure inline", "n/e/d  new / edit / delete", "t      test provider /models and import", "r      refresh current tab", "+ / _  cycle screen layout · H/L horizontal scroll", ":      command palette", "q      quit", "", muted.Render("Each tab retains its cursor, filter and selection."))
	default:
		if m.mode == modeCommand {
			message := m.command.View()
			if m.commandErr != "" {
				message += "\n" + errorStyle.Render(m.commandErr)
			}
			return lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Bold(true).Render("Command"), "", message, "", muted.Render("refresh · new · edit · delete · help · quit"))
		}
		if m.lastError != "" {
			return lipgloss.JoinVertical(lipgloss.Left, errorStyle.Bold(true).Render("Request error"), "", errorStyle.Render(m.lastError))
		}
		return m.detail.View()
	}
}

func (m *app) renderForm() string {
	if m.form == nil {
		return ""
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Render(m.form.title), ""}
	for index, spec := range m.form.fields {
		marker := "  "
		if !m.form.memberMode && index == m.form.active {
			marker = "› "
		}
		lines = append(lines, selectedStyle.Render(marker)+muted.Render(spec.label), "   "+m.form.inputs[index].View())
	}
	if m.form.hasMembers() {
		lines = append(lines, "", lipgloss.NewStyle().Bold(true).Render("Ordered model members"))
		members := m.form.memberLabels()
		if len(members) == 0 {
			lines = append(lines, muted.Render("  no members yet — add a model or source route"))
		}
		for index, member := range members {
			marker := "  "
			if index == m.form.memberCursor && m.form.memberMode {
				marker = "› "
			}
			lines = append(lines, marker+fmt.Sprintf("%d. %s", index+1, member))
		}
		if m.form.addingMember {
			lines = append(lines, "", selectedStyle.Render("Add member  ")+m.form.memberInput.View())
		}
		lines = append(lines, "", muted.Render("tab to members · a add · d remove · K/J reorder"))
	}
	lines = append(lines, "", keyLine("tab / shift+tab", "next / previous"), keyLine("ctrl+s", "save"), keyLine("esc", "cancel"))
	return strings.Join(lines, "\n")
}

func (m *app) renderFooter(width int) string {
	status := m.status
	if count := m.selectedCount(); count > 0 {
		status = fmt.Sprintf("%s · %d selected", status, count)
	}
	if m.loading {
		status = "●  " + status
	}
	if m.mode == modeCommand {
		return lipgloss.NewStyle().Width(width).Render(m.command.View())
	}
	if m.mode == modeHelp {
		return lipgloss.NewStyle().Width(width).Render(muted.Render("? / esc  close help"))
	}
	if m.mode == modeForm || m.mode == modeSourcePicker {
		return lipgloss.NewStyle().Width(width).Render(muted.Render("tab/shift+tab move  ·  Space selects members  ·  ctrl+s save  ·  esc cancel"))
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Width(width).Render(muted.Render(status)),
		lipgloss.NewStyle().Width(width).Render(m.help.View(keyHelp{app: m})),
	)
}

func (m *app) String() string { return m.render() }

type itemDelegate struct{ selected map[string]bool }

func (itemDelegate) Height() int                         { return 1 }
func (itemDelegate) Spacing() int                        { return 0 }
func (itemDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(writer io.Writer, model list.Model, index int, item list.Item) {
	value, ok := item.(entry)
	if !ok {
		return
	}
	marker, selectionMark := "  ", "[ ]"
	titleStyle := lipgloss.NewStyle()
	if index == model.Index() {
		marker = "› "
		titleStyle = selectedStyle
	}
	if d.selected[value.key] {
		selectionMark = "[x]"
	}
	width := max(8, model.Width()-8)
	title := ansi.Truncate(value.title, width, "…")
	_, _ = fmt.Fprint(writer, titleStyle.Render(marker+selectionMark+" "+title))
}

func panel(title, content string, width, height int, focused bool) string {
	title = ansi.Truncate(title, max(6, width-4), "…")
	style := lipgloss.NewStyle().Width(max(1, width)).Height(max(1, height-2)).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#444444")).Padding(0, 1)
	if focused {
		style = style.BorderForeground(lipgloss.Color("#A0A0A0"))
	}
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Bold(true).Render(title), content))
}

func keyLine(keyName, description string) string {
	return lipgloss.NewStyle().Bold(true).Render(keyName) + "  " + muted.Render(description)
}

var (
	muted         = lipgloss.NewStyle().Foreground(lipgloss.Color("#777777"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#C07070"))
)
