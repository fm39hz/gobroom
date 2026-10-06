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
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/kernel"
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
	sectionLogs
	sectionDiscovered
	sectionPhysical
	sectionComboModels
	sectionStrategies
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
	{id: sectionLogs, label: "Logs", method: "logs.list"},
	{id: sectionDiscovered, label: "Discovered", method: "discovered_models.list"},
	{id: sectionPhysical, label: "Physical", method: "physical_models.list"},
	{id: sectionComboModels, label: "Combos", method: "combo_models.list"},
	{id: sectionStrategies, label: "Strategies", method: "strategies.list"},
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
		{id: dashboardRuntime, key: "logs", label: "Logs", sources: []sectionID{sectionLogs}, view: "logs"},
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
	modeStrategyPicker
	modeModelImport
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
			bindings = append(bindings, key.NewBinding(key.WithKeys("e/t/i/d"), key.WithHelp("e/t/i/d", "edit/test/review import/delete")))
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
		bindings = append(bindings, key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new connection")), key.NewBinding(key.WithKeys("t/i"), key.WithHelp("t/i", "test/review-import selected connection")))
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
		{key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "toggle model exposure")), key.NewBinding(key.WithKeys("t/i"), key.WithHelp("t/i", "test/preview · selectively import"))},
	}
}

type app struct {
	ipcPath string
	width   int
	height  int

	panels                []dashboardPanel
	activePanel           int
	activeTabs            []int
	screenMode            int
	focus                 focusPane
	mode                  appMode
	dashboard             viewport.Model
	detail                viewport.Model
	help                  help.Model
	picker                list.Model
	memberPicker          list.Model
	strategyPicker        list.Model
	importPicker          list.Model
	pendingStrategyPicker bool
	form                  *formState
	command               textinput.Model
	commandErr            string

	providers           []providerNode
	providerContextID   string
	strategyCatalog     []kernel.StrategyDefinition
	importPreview       discovery.ConnectionModelsPreview
	importSelected      map[string]bool
	importExisting      map[string]bool
	importBaselineKnown bool
	raw                 map[sectionID]json.RawMessage
	pickerSelected      map[string]bool
	pickerMemberOrder   []modelReference
	pickerOrderPos      map[string]int
	memberItems         []entry
	pickerFocus         int // 0 = ordered members, 1 = candidates
	pickerPreview       string
	sourceItems         []entry
	pickerTarget        string
	pickerModel         any
	pickerSourceRefs    map[string]routeReference

	status            string
	previewExtra      string
	previewTitle      string
	previewKey        string
	pendingPreviewKey string
	lastError         string
	loading           bool
	confirmTitle      string
	confirmMethod     string
	confirmParams     map[string]any
	confirmAction     string
	lastG             bool
}

func newApp(ipcPath string) *app {
	var panels []dashboardPanel
	for _, block := range dashboardPanes {
		for _, definition := range block.tabs {
			selected := map[string]bool{}
			rows := list.New([]list.Item{}, itemDelegate{selected: selected}, 40, 2)
			rows.Filter = contextFilter
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
	pickerSourceRefs := map[string]routeReference{}
	pickerOrderPos := map[string]int{}
	strategySelected := map[string]bool{}
	importSelected := map[string]bool{}
	importExisting := map[string]bool{}
	picker := list.New([]list.Item{}, itemDelegate{selected: pickerSelected, ordered: pickerOrderPos}, 60, 12)
	memberPicker := list.New([]list.Item{}, itemDelegate{selected: pickerSelected, ordered: pickerOrderPos}, 30, 12)
	strategyPicker := list.New([]list.Item{}, itemDelegate{selected: strategySelected}, 60, 12)
	importPicker := list.New([]list.Item{}, itemDelegate{selected: importSelected}, 60, 12)
	picker.Filter = contextFilter
	memberPicker.Filter = contextFilter
	strategyPicker.Filter = contextFilter
	importPicker.Filter = contextFilter
	picker.SetShowTitle(false)
	memberPicker.SetShowTitle(false)
	strategyPicker.SetShowTitle(false)
	importPicker.SetShowTitle(false)
	picker.SetShowFilter(true)
	memberPicker.SetShowFilter(true)
	strategyPicker.SetShowFilter(true)
	importPicker.SetShowFilter(true)
	picker.SetShowStatusBar(false)
	memberPicker.SetShowStatusBar(false)
	strategyPicker.SetShowStatusBar(false)
	importPicker.SetShowStatusBar(false)
	picker.SetShowPagination(false)
	memberPicker.SetShowPagination(false)
	strategyPicker.SetShowPagination(false)
	importPicker.SetShowPagination(false)
	picker.SetShowHelp(false)
	memberPicker.SetShowHelp(false)
	strategyPicker.SetShowHelp(false)
	importPicker.SetShowHelp(false)
	picker.DisableQuitKeybindings()
	memberPicker.DisableQuitKeybindings()
	strategyPicker.DisableQuitKeybindings()
	importPicker.DisableQuitKeybindings()
	picker.Styles = list.DefaultStyles(true)
	memberPicker.Styles = list.DefaultStyles(true)
	strategyPicker.Styles = list.DefaultStyles(true)
	importPicker.Styles = list.DefaultStyles(true)
	command := textinput.New()
	command.Prompt = ":"
	command.CharLimit = 160
	command.Blur()
	activeTabs := make([]int, len(dashboardPanes))
	activeTabs[dashboardConnections] = 1
	model := &app{
		ipcPath:          ipcPath,
		panels:           panels,
		activePanel:      int(dashboardConnections),
		activeTabs:       activeTabs,
		focus:            focusDashboard,
		dashboard:        viewport.New(),
		detail:           viewport.New(),
		help:             help.New(),
		picker:           picker,
		memberPicker:     memberPicker,
		strategyPicker:   strategyPicker,
		importPicker:     importPicker,
		command:          command,
		raw:              map[sectionID]json.RawMessage{},
		pickerSelected:   pickerSelected,
		pickerSourceRefs: pickerSourceRefs,
		pickerOrderPos:   pickerOrderPos,
		importSelected:   importSelected,
		importExisting:   importExisting,
		status:           "connecting to daemon…",
		loading:          true,
	}
	return model
}

// contextFilter treats provider prefixes and slash-qualified upstream IDs as
// literal route selectors. Fuzzy matching remains useful for ordinary model
// and role names, but an `orca/` query must not return unrelated rows merely
// because their inspector text contains those letters in different words.
func contextFilter(term string, targets []string) []list.Rank {
	query := strings.ToLower(strings.TrimSpace(term))
	if !strings.Contains(query, "/") {
		return list.DefaultFilter(term, targets)
	}
	result := make([]list.Rank, 0)
	for index, target := range targets {
		if strings.Contains(strings.ToLower(target), query) {
			result = append(result, list.Rank{Index: index})
		}
	}
	return result
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

func (m *app) rebuildStrategyPicker() {
	items := make([]list.Item, 0, len(m.strategyCatalog))
	for _, definition := range m.strategyCatalog {
		items = append(items, strategyEntry(definition))
	}
	_ = m.strategyPicker.SetItems(items)
}

func strategyEntry(definition kernel.StrategyDefinition) entry {
	lines := []string{definition.Label, definition.Description}
	if len(definition.Options) == 0 {
		lines = append(lines, "Options: none")
	} else {
		lines = append(lines, "Options:")
		for _, option := range definition.Options {
			lines = append(lines, fmt.Sprintf("  %s (%s), default=%v, min=%d, max=%d — %s", option.Key, option.Type, option.Default, option.Minimum, option.Maximum, option.Description))
		}
	}
	return entry{key: definition.ID, title: definition.Label, summary: definition.ID, detail: strings.Join(lines, "\n"), payload: definition}
}

func (m *app) openStrategyPicker() tea.Cmd {
	if len(m.strategyCatalog) == 0 {
		m.status = "loading strategy catalog…"
		m.pendingStrategyPicker = true
		return m.fetch(sectionStrategies)
	}
	m.strategyPicker.GoToStart()
	if m.form != nil && m.form.active >= 0 && m.form.active < len(m.form.fields) {
		current := m.form.inputs[m.form.active].Value()
		for index, raw := range m.strategyPicker.VisibleItems() {
			definition, ok := raw.(entry)
			if ok && definition.key == current {
				m.strategyPicker.Select(index)
				break
			}
		}
	}
	m.mode = modeStrategyPicker
	m.resize()
	return nil
}

func (m *app) updateStrategyPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyText := msg.String()
	if m.strategyPicker.FilterState() == list.Filtering {
		updated, cmd := m.strategyPicker.Update(msg)
		m.strategyPicker = updated
		return m, cmd
	}
	switch keyText {
	case "esc", "q":
		m.mode = modeForm
		m.resize()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		selected, ok := m.strategyPicker.SelectedItem().(entry)
		if !ok || m.form == nil {
			return m, nil
		}
		definition := selected.payload.(kernel.StrategyDefinition)
		strategyField, optionsField := -1, -1
		for index, field := range m.form.fields {
			if field.key == "strategy" || field.key == "policy" {
				strategyField = index
				if field.key == "strategy" {
					optionsField = formFieldIndex(m.form, "strategyOptions")
				} else {
					optionsField = formFieldIndex(m.form, "policyOptions")
				}
				break
			}
		}
		if strategyField < 0 {
			return m, nil
		}
		previous := m.form.inputs[strategyField].Value()
		m.form.inputs[strategyField].SetValue(definition.ID)
		if previous != definition.ID && optionsField >= 0 {
			options, err := kernel.DefaultStrategyConfig(definition.ID)
			if err != nil {
				m.status = err.Error()
				return m, nil
			}
			data, err := json.Marshal(options)
			if err != nil {
				m.status = "encode strategy defaults: " + err.Error()
				return m, nil
			}
			m.form.inputs[optionsField].SetValue(string(data))
		}
		m.form.focus(strategyField)
		m.mode = modeForm
		m.status = "selected strategy: " + definition.ID
		m.resize()
		return m, nil
	}
	updated, cmd := m.strategyPicker.Update(msg)
	m.strategyPicker = updated
	return m, cmd
}

func formFieldIndex(form *formState, key string) int {
	for index, field := range form.fields {
		if field.key == key {
			return index
		}
	}
	return -1
}

func (m *app) updateModelImportKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyText := msg.String()
	if m.importPicker.FilterState() == list.Filtering {
		updated, cmd := m.importPicker.Update(msg)
		m.importPicker = updated
		return m, cmd
	}
	if msg.Key().Code == tea.KeySpace {
		if item, ok := m.importPicker.SelectedItem().(entry); ok {
			m.importSelected[item.key] = !m.importSelected[item.key]
		}
		return m, nil
	}
	switch keyText {
	case "esc", "q":
		m.mode = modeBrowse
		m.status = "model import cancelled; catalog unchanged"
		m.resize()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "a":
		for _, raw := range m.importPicker.VisibleItems() {
			if item, ok := raw.(entry); ok {
				m.importSelected[item.key] = true
			}
		}
		return m, nil
	case "A":
		clear(m.importSelected)
		return m, nil
	case "enter":
		selected := make([]string, 0, len(m.importSelected))
		for _, model := range m.importPreview.Models {
			if m.importSelected[model.ID] {
				selected = append(selected, model.ID)
			}
		}
		m.mode, m.loading = modeBrowse, true
		if len(selected) == 0 {
			m.status = "applying account availability without importing route IDs…"
		} else {
			m.status = fmt.Sprintf("importing %d selected model IDs with %s…", len(selected), m.importPreview.ConnectionID)
		}
		m.resize()
		return m, m.invoke("providers.refresh_models", map[string]any{"nodeID": m.importPreview.ProviderNodeID, "connectionID": m.importPreview.ConnectionID, "modelIDs": selected})
	}
	updated, cmd := m.importPicker.Update(msg)
	m.importPicker = updated
	return m, cmd
}

func (m *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.resize()
		return m, nil
	case fetchMsg:
		if message.section == sectionStrategies {
			if message.err != nil {
				m.pendingStrategyPicker = false
				m.status = "strategy catalog unavailable: " + message.err.Error()
				return m, nil
			}
			if err := json.Unmarshal(message.result, &m.strategyCatalog); err != nil {
				m.pendingStrategyPicker = false
				m.status = "decode strategy catalog: " + err.Error()
				return m, nil
			}
			m.rebuildStrategyPicker()
			if m.pendingStrategyPicker {
				m.pendingStrategyPicker = false
				if m.form == nil || m.mode != modeForm {
					return m, nil
				}
				m.mode = modeStrategyPicker
				m.resize()
			}
			return m, nil
		}
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
			m.pendingPreviewKey = ""
			m.mode = modeBrowse
			m.form = nil
			m.syncDetail()
			return m, nil
		}
		if message.method == "connections.preview_models" {
			var preview discovery.ConnectionModelsPreview
			if err := json.Unmarshal(message.result, &preview); err != nil {
				m.status = "decode connection model preview: " + err.Error()
				m.syncDetail()
				return m, nil
			}
			m.importPreview = preview
			clear(m.importSelected)
			clear(m.importExisting)
			m.importBaselineKnown = len(m.raw[sectionModels]) > 0
			known := map[string]bool{}
			var catalog []catalogModel
			if err := json.Unmarshal(m.raw[sectionModels], &catalog); err == nil {
				for _, model := range catalog {
					if model.NodeID == preview.ProviderNodeID {
						known[model.ExternalID] = true
					}
				}
			}
			items := make([]list.Item, 0, len(preview.Models))
			for _, model := range preview.Models {
				detail := fmt.Sprintf("Upstream model ID\n%s\n\nDisplay name\n%s\n\nCapabilities\n%s", model.ID, model.DisplayName, pretty(model.Profile))
				item := entry{key: model.ID, title: model.ID, summary: model.DisplayName, detail: detail, payload: model}
				if known[model.ID] {
					m.importExisting[model.ID] = true
					item.summary += " · already imported"
					item.title += "  · already imported"
				} else {
					item.summary += " · new route"
					item.title += "  · new"
				}
				items = append(items, item)
				m.importSelected[model.ID] = true
			}
			_ = m.importPicker.SetItems(items)
			m.importPicker.ResetFilter()
			m.importPicker.GoToStart()
			m.mode = modeModelImport
			m.loading = false
			m.lastError = ""
			if m.importBaselineKnown {
				m.status = fmt.Sprintf("reviewing %d IDs · all checked; deselect routes not to import", len(preview.Models))
			} else {
				m.status = "catalog baseline not loaded · all returned IDs checked; verify before apply"
			}
			m.resize()
			return m, nil
		}
		m.mode = modeBrowse
		m.form = nil
		m.lastError = ""
		m.status = message.method + " completed"
		m.previewExtra, m.previewTitle = "", ""
		m.previewKey, m.pendingPreviewKey = m.pendingPreviewKey, ""
		if message.method == "routes.explain" {
			m.previewExtra = pretty(json.RawMessage(message.result))
			m.previewTitle = "Effective route order"
		}
		if message.method == "connections.test" {
			var result struct {
				Endpoint    string `json:"endpoint"`
				ModelsFound int    `json:"modelsFound"`
				Truncated   bool   `json:"truncated"`
			}
			if json.Unmarshal(message.result, &result) == nil {
				m.previewExtra = pretty(message.result)
				m.previewTitle = "Connection model-list test · no catalog changes"
				m.status = fmt.Sprintf("connection test passed · %d models from %s", result.ModelsFound, result.Endpoint)
				if result.Truncated {
					m.status += " · preview truncated"
				}
			}
		}
		if message.method == "providers.refresh_models" {
			var result struct {
				Models    int    `json:"models"`
				Available int    `json:"available"`
				Complete  bool   `json:"complete"`
				URL       string `json:"url"`
			}
			if json.Unmarshal(message.result, &result) == nil {
				m.status = fmt.Sprintf("imported %d selected IDs · %d available in %s", result.Models, result.Available, result.URL)
				if !result.Complete {
					m.status += " · incomplete catalog: absence remains unknown"
				}
			}
		}
		m.syncDetail()
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
			m.pendingStrategyPicker = false
			m.mode = modeBrowse
			return m, nil
		}
		if keyText == "enter" && m.form.active >= 0 && m.form.active < len(m.form.fields) {
			key := m.form.fields[m.form.active].key
			if key == "strategy" || key == "policy" {
				return m, m.openStrategyPicker()
			}
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
			m.pendingStrategyPicker = false
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
	if m.mode == modeStrategyPicker {
		return m.updateStrategyPickerKey(msg)
	}
	if m.mode == modeModelImport {
		return m.updateModelImportKey(msg)
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
	case "m":
		return m, m.openTypedMemberPicker()
	case "x":
		selected := m.selectedEntry()
		if selected == nil || !selected.exposed || selected.publicName == "" {
			m.status = "explain order only for an exposed model"
			return m, nil
		}
		m.status = "loading effective route order…"
		m.pendingPreviewKey = selected.key
		return m, m.invoke("routes.explain", map[string]any{"model": selected.publicName})
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
			if selected := m.selectedEntry(); selected != nil {
				switch value := selected.payload.(type) {
				case providerNode:
					return m, m.testAndDiscover()
				case connection:
					return m, m.testConnection(value)
				}
			}
		}
	case "i":
		if current.definition.id == dashboardConnections {
			if selected := m.selectedEntry(); selected != nil {
				if value, ok := selected.payload.(connection); ok {
					return m, m.previewConnectionModels(value)
				}
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
					m.form = newConnectionForm(node)
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
				result = append(result, userAssertedAlias(route.ID))
			}
		}
	}
	if len(result) == 0 {
		if selected := m.selectedEntry(); selected != nil {
			if route, ok := selected.payload.(discoveredRoute); ok {
				result = append(result, userAssertedAlias(route.ID))
			}
		}
	}
	return result
}

func userAssertedAlias(routeID string) routeReference {
	return routeReference{RouteID: routeID, Fidelity: "alias", Evidence: []map[string]any{{"source": "user_assertion", "confidence": 0.5, "note": "manually grouped as the same Physical model"}}}
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
	case "logs":
		items, err = makeEntries("logs.list", m.raw[sectionLogs], m.providers)
	case "health":
		items, err = makeEntries("health.list", m.raw[sectionHealth], m.providers)
	case "unavailable-usage":
		items = []entry{{key: "usage-unavailable", title: "Usage API unavailable", summary: "daemon has no usage read endpoint", detail: "Usage data is not available through the current daemon IPC contract."}}
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
		actions = append(actions, "e            edit connection", "t            test selected account", "i            import models with this account", "d            delete connection")
	case discoveredRoute:
		actions = append(actions, "Space        select route", "f            build Physical from selection")
	case physicalModel:
		actions = append(actions, "e            edit policy", "m            edit source candidates", "p            toggle /v1/models exposure", "x            explain effective route order", "c            compose into Combo", "d            delete Physical")
	case comboModel:
		actions = append(actions, "e            edit strategy", "m            edit members", "p            toggle /v1/models exposure", "x            explain effective route order", "c            compose nested Combo", "d            delete Combo")
	}
	preview := selected.detail
	if m.previewExtra != "" && (m.previewKey == "" || m.previewKey == selected.key) {
		title := m.previewTitle
		if title == "" {
			title = "Action result"
		}
		preview += "\n\n" + title + "\n" + m.previewExtra
	}
	return lipgloss.JoinVertical(lipgloss.Left, preview, "", muted.Render("Actions"), muted.Render(strings.Join(actions, "\n")))
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
	pickerWidth := max(20, rightWidth-6)
	pickerHeight := max(4, bodyHeight-6)
	if m.pickerTarget == "combo" {
		memberWidth := max(16, pickerWidth*36/100)
		candidateWidth := max(16, pickerWidth-memberWidth-3)
		listHeight := max(3, m.comboPickerPaneHeight()-3)
		m.memberPicker.SetSize(max(10, memberWidth-4), listHeight)
		m.picker.SetSize(max(10, candidateWidth-4), listHeight)
	} else {
		m.picker.SetSize(pickerWidth, pickerHeight)
	}
	if m.mode == modeStrategyPicker {
		m.strategyPicker.SetSize(max(10, rightWidth-8), max(4, bodyHeight-13))
	} else {
		m.strategyPicker.SetSize(pickerWidth, pickerHeight)
	}
	m.importPicker.SetSize(max(10, rightWidth-8), max(4, bodyHeight-13))
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
	clear(m.pickerSourceRefs)
	m.pickerMemberOrder = nil
	m.syncPickerOrderPositions()
	m.mode = modeSourcePicker
	m.picker.GoToStart()
	m.resize()
	return nil
}

func (m *app) openTypedMemberPicker() tea.Cmd {
	selected := m.selectedEntry()
	if selected == nil {
		m.status = "focus a Physical or Combo first"
		return nil
	}
	workspace, err := (modelWorkspaceClient{providers: m.providers}).Build(m.raw[sectionDiscovered], m.raw[sectionPhysical], m.raw[sectionComboModels])
	if err != nil {
		m.status = err.Error()
		return nil
	}
	clear(m.pickerSelected)
	clear(m.pickerSourceRefs)
	m.pickerMemberOrder = nil
	m.syncPickerOrderPositions()
	switch value := selected.payload.(type) {
	case physicalModel:
		m.pickerTarget, m.pickerModel = "physical", value
		m.sourceItems = make([]entry, 0, len(workspace.Discovered))
		for _, item := range workspace.Discovered {
			if route, ok := item.payload.(discoveredRoute); ok {
				for _, source := range value.Sources {
					if source.RouteID == route.ID {
						m.pickerSelected[item.key] = true
						m.pickerSourceRefs[item.key] = source
					}
				}
				m.sourceItems = append(m.sourceItems, item)
			}
		}
	case comboModel:
		m.pickerTarget, m.pickerModel = "combo", value
		m.pickerMemberOrder = append(m.pickerMemberOrder, value.Members...)
		m.sourceItems = append(append([]entry(nil), workspace.Physical...), workspace.Combos...)
		for index := range m.sourceItems {
			item := &m.sourceItems[index]
			item.key = modelReferenceKey(modelReference{Kind: item.modelKind, ID: item.modelRef})
			for _, member := range m.pickerMemberOrder {
				if member.ID == item.modelRef && member.Kind == item.modelKind {
					m.pickerSelected[item.key] = true
					break
				}
			}
		}
		m.syncPickerOrderPositions()
	default:
		m.status = "focus a typed Physical or Combo first"
		return nil
	}
	m.mode = modeSourcePicker
	m.picker.GoToStart()
	converted := make([]list.Item, 0, len(m.sourceItems))
	for _, item := range m.sourceItems {
		converted = append(converted, item)
	}
	pickerFilterState := m.picker.FilterState()
	pickerFilterText := m.picker.FilterInput.Value()
	_ = m.picker.SetItems(converted)
	if pickerFilterState != list.Unfiltered {
		m.picker.SetFilterText(pickerFilterText)
		if pickerFilterState == list.Filtering {
			m.picker.SetFilterState(list.Filtering)
		}
	}
	if m.pickerTarget == "combo" {
		m.rebuildComboMemberItems(modelReference{})
		m.pickerFocus = 1
	}
	m.pickerPreview = ""
	m.resize()
	return nil
}

func (m *app) updateSourcePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pickerTarget == "combo" {
		return m.updateComboPickerKey(msg)
	}
	keyText := msg.String()
	if m.picker.FilterState() == list.Filtering {
		updated, cmd := m.picker.Update(msg)
		m.picker = updated
		return m, cmd
	}
	switch keyText {
	case "esc", "q":
		m.mode = modeBrowse
		m.pickerTarget = ""
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if m.pickerTarget == "physical" {
			value := m.pickerModel.(physicalModel)
			sources := make([]routeReference, 0)
			for _, item := range m.sourceItems {
				if m.pickerSelected[item.key] {
					if route, ok := item.payload.(discoveredRoute); ok {
						source := m.pickerSourceRefs[item.key]
						source.RouteID = route.ID
						if source.Fidelity == "" {
							source = userAssertedAlias(route.ID)
						}
						sources = append(sources, source)
					}
				}
			}
			if len(sources) == 0 {
				m.status = "select at least one source"
				return m, nil
			}
			m.mode, m.pickerTarget, m.loading = modeBrowse, "", true
			return m, m.invoke("physical_models.upsert", map[string]any{"name": value.Name, "identity": value.Identity, "sources": sources, "policy": value.Policy, "profile": value.Profile, "limits": value.Limits, "reasoning": value.Reasoning, "allowCompatibleSources": value.AllowCompatibleSources, "allowDynamicSources": value.AllowDynamicSources, "discoverable": value.Discoverable, "enabled": value.Enabled})
		}
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
		m.form = newPhysicalModelForm(selectedRouteRefsFromEntries(m.sourceItems, m.pickerSelected))
		m.form.title = "Create physical model"
		m.mode = modeForm
		m.resize()
		return m, nil
	}
	if msg.Key().Code == tea.KeySpace {
		if source, ok := m.picker.SelectedItem().(entry); ok {
			selected := !m.pickerSelected[source.key]
			m.pickerSelected[source.key] = selected
			if !selected {
				delete(m.pickerSelected, source.key)
				delete(m.pickerSourceRefs, source.key)
			} else if route, ok := source.payload.(discoveredRoute); ok && m.pickerTarget == "physical" {
				m.pickerSourceRefs[source.key] = userAssertedAlias(route.ID)
			}
		}
		return m, nil
	}
	if keyText == "f" && m.pickerTarget == "physical" {
		if source, ok := m.picker.SelectedItem().(entry); ok && m.pickerSelected[source.key] {
			item := m.pickerSourceRefs[source.key]
			cycle := map[string]string{"alias": "exact", "exact": "compatible", "compatible": "dynamic", "dynamic": "unknown", "unknown": "alias", "": "exact"}
			item.Fidelity = cycle[item.Fidelity]
			if item.Fidelity == "alias" && len(item.Evidence) == 0 {
				item.Evidence = userAssertedAlias(item.RouteID).Evidence
			}
			m.pickerSourceRefs[source.key] = item
			m.status = "source fidelity: " + item.Fidelity
		}
		return m, nil
	}
	updated, cmd := m.picker.Update(msg)
	m.picker = updated
	return m, cmd
}

func (m *app) comboPickerPaneHeight() int {
	bodyHeight := max(5, m.height-7)
	if m.pickerPreview != "" {
		return max(6, bodyHeight-11)
	}
	return max(7, bodyHeight-6)
}

func selectedRouteRefsFromEntries(items []entry, selected map[string]bool) []routeReference {
	refs := make([]routeReference, 0)
	for _, item := range items {
		if selected[item.key] {
			if route, ok := item.payload.(discoveredRoute); ok {
				refs = append(refs, userAssertedAlias(route.ID))
			}
		}
	}
	return refs
}

func modelReferenceKey(value modelReference) string { return value.Kind + "\x00" + value.ID }

func (m *app) comboMembersFromPicker() []modelReference {
	return append([]modelReference(nil), m.pickerMemberOrder...)
}

func (m *app) rebuildComboMemberItems(target modelReference) {
	selectedKey := ""
	if target.ID != "" {
		selectedKey = modelReferenceKey(target)
	} else if selected, ok := m.memberPicker.SelectedItem().(entry); ok {
		selectedKey = modelReferenceKey(modelReference{Kind: selected.modelKind, ID: selected.modelRef})
	}
	byReference := make(map[string]entry, len(m.sourceItems))
	for _, item := range m.sourceItems {
		byReference[modelReferenceKey(modelReference{Kind: item.modelKind, ID: item.modelRef})] = item
	}
	m.memberItems = make([]entry, 0, len(m.pickerMemberOrder))
	for _, member := range m.pickerMemberOrder {
		key := modelReferenceKey(member)
		item, ok := byReference[key]
		if !ok {
			item = entry{key: key, title: member.Kind + ":" + member.ID, summary: "not in the current model catalog", modelRef: member.ID, modelKind: member.Kind}
		}
		if comboUsesWeights(m.pickerModel) {
			weight := member.Weight
			if weight < 1 {
				weight = 1
			}
			item.title += fmt.Sprintf(" [w=%d]", weight)
		}
		m.memberItems = append(m.memberItems, item)
	}
	converted := make([]list.Item, 0, len(m.memberItems))
	for _, item := range m.memberItems {
		converted = append(converted, item)
	}
	filterState := m.memberPicker.FilterState()
	filterText := m.memberPicker.FilterInput.Value()
	_ = m.memberPicker.SetItems(converted)
	if filterState != list.Unfiltered {
		m.memberPicker.SetFilterText(filterText)
		if filterState == list.Filtering {
			m.memberPicker.SetFilterState(list.Filtering)
		}
	}
	visible := m.memberPicker.VisibleItems()
	if selectedKey != "" {
		for index, raw := range visible {
			item, ok := raw.(entry)
			if ok && modelReferenceKey(modelReference{Kind: item.modelKind, ID: item.modelRef}) == selectedKey {
				m.memberPicker.Select(index)
				return
			}
		}
	}
	if len(visible) > 0 {
		m.memberPicker.Select(min(m.memberPicker.Index(), len(visible)-1))
	}
}

func (m *app) updateComboPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyText := msg.String()
	focused := &m.picker
	if m.pickerFocus == 0 {
		focused = &m.memberPicker
	}
	if focused.FilterState() == list.Filtering {
		updated, cmd := focused.Update(msg)
		*focused = updated
		return m, cmd
	}
	if msg.Key().Code == tea.KeySpace {
		if m.pickerFocus == 0 {
			item, ok := m.memberPicker.SelectedItem().(entry)
			if !ok {
				return m, nil
			}
			ref := modelReference{Kind: item.modelKind, ID: item.modelRef}
			m.removePickerMember(ref)
			delete(m.pickerSelected, modelReferenceKey(ref))
			m.syncPickerOrderPositions()
			m.rebuildComboMemberItems(modelReference{})
			return m, nil
		}
		item, ok := m.picker.SelectedItem().(entry)
		if !ok {
			return m, nil
		}
		ref := modelReference{Kind: item.modelKind, ID: item.modelRef}
		if m.pickerSelected[item.key] {
			delete(m.pickerSelected, item.key)
			m.removePickerMember(ref)
		} else {
			if comboUsesWeights(m.pickerModel) {
				ref.Weight = 1
			}
			m.pickerSelected[item.key] = true
			m.pickerMemberOrder = append(m.pickerMemberOrder, ref)
		}
		m.syncPickerOrderPositions()
		m.rebuildComboMemberItems(modelReference{})
		return m, nil
	}
	switch keyText {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		if m.pickerPreview != "" {
			m.pickerPreview = ""
			m.resize()
			return m, nil
		}
		m.mode, m.pickerTarget = modeBrowse, ""
		m.status = "Combo edit cancelled"
		m.resize()
		return m, nil
	case "h", "left":
		m.pickerFocus = 0
		return m, nil
	case "l", "right":
		m.pickerFocus = 1
		return m, nil
	case "enter":
		if item, ok := focused.SelectedItem().(entry); ok {
			m.pickerPreview = item.detail
			m.resize()
		}
		return m, nil
	case "ctrl+s":
		value := m.pickerModel.(comboModel)
		members := m.comboMembersFromPicker()
		if len(members) == 0 {
			m.status = "select at least one member"
			return m, nil
		}
		m.mode, m.pickerTarget, m.loading = modeBrowse, "", true
		m.status = "saving Combo member order…"
		m.resize()
		return m, m.invoke("combo_models.upsert", map[string]any{"name": value.Name, "members": members, "strategy": value.Strategy, "reasoning": value.Reasoning, "discoverable": value.Discoverable, "enabled": value.Enabled})
	case "+", "=", "-":
		if m.pickerFocus != 0 {
			return m, nil
		}
		if !comboUsesWeights(m.pickerModel) {
			m.status = "member weights are used by weighted-fallback"
			return m, nil
		}
		delta := 1
		if keyText == "-" {
			delta = -1
		}
		if item, ok := m.memberPicker.SelectedItem().(entry); ok {
			m.changePickerMemberWeight(modelReference{Kind: item.modelKind, ID: item.modelRef}, delta)
		}
		return m, nil
	case "K", "shift+k", "J", "shift+j":
		if m.pickerFocus == 0 {
			if item, ok := m.memberPicker.SelectedItem().(entry); ok {
				delta := -1
				if keyText == "J" || keyText == "shift+j" {
					delta = 1
				}
				ref := modelReference{Kind: item.modelKind, ID: item.modelRef}
				m.movePickerMember(ref, delta)
				m.rebuildComboMemberItems(ref)
			}
			return m, nil
		}
	}
	updated, cmd := focused.Update(msg)
	*focused = updated
	return m, cmd
}

func comboUsesWeights(model any) bool {
	value, ok := model.(comboModel)
	if !ok {
		return false
	}
	return value.Strategy.ID == "weighted-fallback"
}

func (m *app) changePickerMemberWeight(target modelReference, delta int) {
	key := modelReferenceKey(target)
	for index, member := range m.pickerMemberOrder {
		if modelReferenceKey(member) != key {
			continue
		}
		weight := member.Weight
		if weight < 1 {
			weight = 1
		}
		member.Weight = max(1, weight+delta)
		m.pickerMemberOrder[index] = member
		m.syncPickerOrderPositions()
		m.rebuildComboMemberItems(target)
		m.status = fmt.Sprintf("member weight: %d", member.Weight)
		return
	}
}

func (m *app) syncPickerOrderPositions() {
	clear(m.pickerOrderPos)
	for index, member := range m.pickerMemberOrder {
		m.pickerOrderPos[modelReferenceKey(member)] = index + 1
	}
}

func (m *app) removePickerMember(target modelReference) {
	key := modelReferenceKey(target)
	for index, member := range m.pickerMemberOrder {
		if modelReferenceKey(member) == key {
			m.pickerMemberOrder = append(m.pickerMemberOrder[:index], m.pickerMemberOrder[index+1:]...)
			return
		}
	}
}

func (m *app) movePickerMember(target modelReference, delta int) {
	key := modelReferenceKey(target)
	for index, member := range m.pickerMemberOrder {
		if modelReferenceKey(member) != key {
			continue
		}
		to := index + delta
		if to < 0 || to >= len(m.pickerMemberOrder) {
			return
		}
		m.pickerMemberOrder[index], m.pickerMemberOrder[to] = m.pickerMemberOrder[to], m.pickerMemberOrder[index]
		m.syncPickerOrderPositions()
		return
	}
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
		return m.invoke("physical_models.upsert", map[string]any{"name": value.Name, "identity": value.Identity, "sources": value.Sources, "policy": value.Policy, "profile": value.Profile, "limits": value.Limits, "reasoning": value.Reasoning, "allowCompatibleSources": value.AllowCompatibleSources, "allowDynamicSources": value.AllowDynamicSources, "discoverable": value.Discoverable, "enabled": value.Enabled})
	case comboModel:
		value.Discoverable = !value.Discoverable
		m.loading = true
		m.status = "updating combo model exposure…"
		return m.invoke("combo_models.upsert", map[string]any{"name": value.Name, "members": value.Members, "strategy": value.Strategy, "reasoning": value.Reasoning, "discoverable": value.Discoverable, "enabled": value.Enabled})
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

func (m *app) testConnection(value connection) tea.Cmd {
	m.loading = true
	m.status = "testing selected connection's model-list endpoint (no import)…"
	m.pendingPreviewKey = value.ID
	return m.invoke("connections.test", map[string]any{"connectionID": value.ID})
}

func (m *app) previewConnectionModels(value connection) tea.Cmd {
	m.loading = true
	m.status = "loading model IDs for selective import review…"
	return m.invoke("connections.preview_models", map[string]any{"connectionID": value.ID})
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
	case "connections.test":
		return nil
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
	if m.mode != modeBrowse {
		body = panel("[0] Main · "+m.active().definition.label, m.renderMainView(), width, bodyHeight, true)
	} else if m.screenMode == 2 {
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
	case modeModelImport:
		selected := 0
		for _, model := range m.importPreview.Models {
			if m.importSelected[model.ID] {
				selected++
			}
		}
		newCount := len(m.importPreview.Models) - len(m.importExisting)
		completeness := "complete list"
		if !m.importPreview.Complete {
			completeness = "incomplete list; absent IDs remain unknown"
		}
		if m.importPreview.Truncated {
			completeness += " · review limit reached"
		}
		catalogSummary := fmt.Sprintf("%d returned · %d new · %d already imported · %d selected · %s", m.importPreview.ModelsFound, newCount, len(m.importExisting), selected, completeness)
		if !m.importBaselineKnown {
			catalogSummary = fmt.Sprintf("%d returned · %d selected · existing catalog not loaded · %s", m.importPreview.ModelsFound, selected, completeness)
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("Review model import · connection "+m.importPreview.ConnectionID),
			muted.Render(catalogSummary),
			muted.Render(m.importPreview.Endpoint),
			m.importPicker.View(),
		)
	case modeStrategyPicker:
		selectedDetail := "Select a registered strategy to inspect its behavior and supported options."
		if selected, ok := m.strategyPicker.SelectedItem().(entry); ok {
			selectedDetail = selected.detail
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("Strategy catalog"),
			muted.Render("j/k navigate · / search · Enter select · Esc return without changing the model"),
			m.strategyPicker.View(),
			panel("Primitive contract", selectedDetail, m.strategyPicker.Width()+4, 8, false),
		)
	case modeSourcePicker:
		if m.pickerTarget == "combo" {
			value := m.pickerModel.(comboModel)
			paneHeight := m.comboPickerPaneHeight()
			memberPane := panel("Ordered members", m.memberPicker.View(), m.memberPicker.Width()+4, paneHeight, m.pickerFocus == 0)
			candidatePane := panel("Physical / Combo candidates", m.picker.View(), m.picker.Width()+4, paneHeight, m.pickerFocus == 1)
			content := []string{
				lipgloss.NewStyle().Bold(true).Render("Edit Combo · " + value.Name),
				muted.Render("h/l switch pane · j/k move · Space add/remove · K/J reorder · +/- weighted member weight · Enter inspect · ctrl+s save · Esc cancel"),
				lipgloss.JoinHorizontal(lipgloss.Top, memberPane, candidatePane),
			}
			if m.pickerPreview != "" {
				lines := strings.Split(m.pickerPreview, "\n")
				if len(lines) > 3 {
					lines = append(lines[:3], "…")
				}
				content = append(content, panel("Model details · Esc closes details", strings.Join(lines, "\n"), memberPaneWidth(m)+candidatePaneWidth(m), 6, false))
			}
			return lipgloss.JoinVertical(lipgloss.Left, content...)
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("Choose provider model variants"),
			muted.Render("Search with / · Space toggles · K/J reorders selected Combo member · f cycles source fidelity · Enter saves"),
			m.picker.View(),
			m.pickerOrderSummary(),
			muted.Render(fmt.Sprintf("%d candidates selected · alias is a manual identity assertion; compatible/dynamic require policy opt-in", len(m.pickerSelected))),
		)
	case modeConfirm:
		return lipgloss.JoinVertical(lipgloss.Left, errorStyle.Bold(true).Render(m.confirmTitle), "", "This change is applied to the daemon immediately.", "", keyLine("y / enter", "confirm"), keyLine("n / esc", "cancel"))
	case modeHelp:
		return lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Bold(true).Render("Keyboard map"), "", "h / l  previous / next block", "j / k  move inside active context", "[ / ]  previous / next tab", "enter  open selected context in main view", "esc    return to parent view", "space  select item", "1–5    focus a block · 0 main view", "/      filter this context · n/N next match", "f      create Physical from discovered routes / cycle fidelity in picker", "m      edit Physical sources or Combo members", "x      explain effective route order", "c      compose selected Physical models", "p      toggle exposure inline", "n/e/d  new / edit / delete", "t      test provider /models and import", "r      refresh current tab", "+ / _  cycle screen layout · H/L horizontal scroll", ":      command palette", "q      quit", "", muted.Render("Each tab retains its cursor, filter and selection."))
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

func memberPaneWidth(m *app) int    { return m.memberPicker.Width() + 4 }
func candidatePaneWidth(m *app) int { return m.picker.Width() + 4 }

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
		label := spec.label
		if spec.key == "strategy" || spec.key == "policy" {
			label += " · Enter opens registered strategy catalog"
		}
		lines = append(lines, selectedStyle.Render(marker)+muted.Render(label), "   "+m.form.inputs[index].View())
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
	if m.form.method == "providers.create" {
		preset := m.form.providerPreset
		if preset == "" {
			preset = "custom"
		}
		lines = append(lines, "", keyLine("ctrl+p", "switch provider preset · "+preset))
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
	if m.mode == modeStrategyPicker {
		return lipgloss.NewStyle().Width(width).Render(muted.Render("j/k move  ·  / filter  ·  Enter select  ·  Esc return to Combo/Physical editor"))
	}
	if m.mode == modeModelImport {
		return lipgloss.NewStyle().Width(width).Render(muted.Render("j/k move  ·  / filter  ·  Space toggle  ·  a select visible  ·  A clear  ·  Enter apply/import  ·  Esc cancel"))
	}
	if m.mode == modeSourcePicker && m.pickerTarget == "combo" {
		pane := "candidates"
		if m.pickerFocus == 0 {
			pane = "ordered members"
		}
		return lipgloss.NewStyle().Width(width).Render(muted.Render("Focus: " + pane + "  ·  h/l pane  ·  j/k move  ·  Space add/remove  ·  K/J reorder  ·  +/- weight  ·  Enter inspect  ·  ctrl+s save  ·  Esc cancel"))
	}
	if m.mode == modeForm || m.mode == modeSourcePicker {
		return lipgloss.NewStyle().Width(width).Render(muted.Render("tab/shift+tab move  ·  Space toggles  ·  K/J reorder Combo members  ·  ctrl+s save  ·  esc cancel"))
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Width(width).Render(muted.Render(status)),
		lipgloss.NewStyle().Width(width).Render(m.help.View(keyHelp{app: m})),
	)
}

func (m *app) String() string { return m.render() }

type itemDelegate struct {
	selected map[string]bool
	ordered  map[string]int
}

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
		if order := d.ordered[modelReferenceKey(modelReference{Kind: value.modelKind, ID: value.modelRef})]; order > 0 {
			selectionMark = fmt.Sprintf("[%d]", order)
		}
	}
	width := max(8, model.Width()-8)
	title := ansi.Truncate(value.title, width, "…")
	_, _ = fmt.Fprint(writer, titleStyle.Render(marker+selectionMark+" "+title))
}

func (m *app) pickerOrderSummary() string {
	if m.pickerTarget != "combo" {
		return muted.Render("Selected candidates retain their selection while filtered")
	}
	if len(m.pickerMemberOrder) == 0 {
		return muted.Render("Ordered Combo members: none")
	}
	labels := make([]string, 0, len(m.pickerMemberOrder))
	for index, member := range m.pickerMemberOrder {
		labels = append(labels, fmt.Sprintf("%d %s:%s", index+1, member.Kind, member.ID))
	}
	return lipgloss.NewStyle().Width(max(1, m.picker.Width())).Render("Order: " + strings.Join(labels, "  →  "))
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
