package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestPhysicalModelEntryUsesProviderPrefixAndKeepsCatalogIDInInspector(t *testing.T) {
	providers := []providerNode{{ID: "node-secret-id", Name: "Orca", Prefix: "orca"}}
	raw := json.RawMessage(`[{
  "ID":"catalog-internal-id",
  "NodeID":"node-secret-id",
  "Kind":"discovered",
  "ExternalID":"orcarouter/free/model-v2",
  "DisplayName":"Free Model",
  "Profile":{"input.image":{"state":"native"}}
}]`)
	items, err := makeEntries("models.list", raw, providers)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d rows, want 1", len(items))
	}
	if got, want := items[0].title, "orca/orcarouter/free/model-v2"; got != want {
		t.Fatalf("physical display = %q, want %q", got, want)
	}
	if items[0].routeRef != items[0].title {
		t.Fatalf("combo reference = %q, want displayed physical route %q", items[0].routeRef, items[0].title)
	}
	if strings.Contains(items[0].title, "catalog-internal-id") || strings.Contains(items[0].summary, "node-secret-id") {
		t.Fatalf("internal IDs leaked into dashboard row: %#v", items[0])
	}
	if !strings.Contains(items[0].detail, "catalog-internal-id") {
		t.Fatal("inspector should retain the catalog ID for diagnostics")
	}
}

func TestQuotaPaneDecodesRuntimeMapContract(t *testing.T) {
	raw := json.RawMessage(`{"node-a|conn-a|model-a|daily":{"ProviderNodeID":"node-a","ConnectionID":"conn-a","ModelRef":"model-a","WindowName":"daily","Used":9,"Remaining":0,"ResetAt":"2026-09-20T00:00:00Z","Source":"provider"}}`)
	items, err := makeEntries("quota.list", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].title != "model-a" || !strings.Contains(items[0].detail, "daily") {
		t.Fatalf("unexpected quota pane rows: %#v", items)
	}
}

func TestConnectionFormMasksSecretAndOmitsUnchangedSecret(t *testing.T) {
	form := newResourceForm(int(sectionConnections), &entry{payload: connection{ID: "conn-1", ProviderNodeID: "node-1", Name: "personal", CredentialType: "api_key", Priority: 100}}, "")
	if form == nil {
		t.Fatal("connection editor was not created")
	}
	for i, field := range form.fields {
		if field.key == "secret" {
			form.inputs[i].SetValue("new-secret-value")
		}
	}
	if got := form.inputs[2].View(); strings.Contains(got, "new-secret-value") {
		t.Fatal("secret input rendered the credential in clear text")
	}
	form.inputs[2].SetValue("")
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := params["secret"]; exists {
		t.Fatal("blank edit should leave the existing credential untouched")
	}
	if params["id"] != "conn-1" {
		t.Fatalf("connection ID not carried as immutable identity: %#v", params)
	}
}

func TestDashboardRendersDistinctLazyGitStyleFramesAndAltScreen(t *testing.T) {
	for _, width := range []int{140, 100, 70} {
		model := newApp("/tmp/gobroom.sock")
		model.width, model.height = width, 40
		model.activePanel = int(dashboardStatus)
		model.status = "ready"
		model.resize()
		view := model.View()
		if !view.AltScreen {
			t.Fatalf("width %d: view did not request the alternate screen", width)
		}
		if !strings.Contains(view.Content, "[1] Status") || !strings.Contains(view.Content, "[2] Providers") || !strings.Contains(view.Content, "[3] Models") {
			t.Fatalf("width %d: dashboard panes are not independently titled:\n%s", width, view.Content)
		}
		if width == 140 && (!strings.Contains(view.Content, "Discovered") || !strings.Contains(view.Content, "Physical") || !strings.Contains(view.Content, "Combos")) {
			t.Fatalf("wide layout omitted model tabs:\n%s", view.Content)
		}
		if got := lipgloss.Width(view.Content); got > width {
			t.Fatalf("width %d: rendered view is %d cells wide", width, got)
		}
		if got := lipgloss.Height(view.Content); got > 40 {
			t.Fatalf("height 40: rendered view is %d rows tall", got)
		}
	}
}

func TestHLChangesPaneAndJKMovesWithinItWithoutActivatingAnotherPane(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.activePanel = int(dashboardConnections)
	model.activeTabs[dashboardConnections] = 1
	model.resize()
	model.setPaneItems(model.activeContextIndex(), []entry{
		{key: "conn-a", title: "first"},
		{key: "conn-b", title: "second"},
	})
	_, _ = model.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if model.activePanel != int(dashboardConnections) {
		t.Fatal("j changed the focused pane instead of moving within it")
	}
	if selected := model.selectedEntry(); selected == nil || selected.key != "conn-b" {
		t.Fatalf("j selection = %#v, want second row", selected)
	}
	_, cmd := model.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd != nil || model.activePanel != int(dashboardModels) {
		t.Fatal("l should focus the next independent dashboard block without fetching it")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.focus != focusInspector {
		t.Fatal("enter should focus the inspector, not activate a dashboard pane")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.focus != focusInspector {
		t.Fatal("enter in the main context must not pop back to its parent")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if model.focus != focusDashboard {
		t.Fatal("esc should return to the parent block")
	}
}

func TestLazyGitBlockNavigationWrapsAndDoesNotEscapeMainContext(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.resize()
	model.activePanel = 0
	_, _ = model.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if model.activePanel != len(dashboardPanes)-1 {
		t.Fatalf("h from first block selected %d, want wrapped last block", model.activePanel)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if model.activePanel != 0 {
		t.Fatalf("l from last block selected %d, want wrapped first block", model.activePanel)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
	if model.focus != focusInspector {
		t.Fatal("0 did not focus the main context")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if model.activePanel != 0 || model.focus != focusInspector {
		t.Fatal("side-block navigation leaked into the main context")
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if model.focus != focusDashboard {
		t.Fatal("Esc did not pop back to the side context")
	}
}

func TestScreenModesFollowNormalHalfFullProgression(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.resize()
	model.screenMode = 1
	half := model.View().Content
	if !strings.Contains(half, "[2] Providers") || !strings.Contains(half, "[0] Main") {
		t.Fatalf("half mode must retain active side and main contexts:\n%s", half)
	}
	model.screenMode = 2
	fullSide := model.View().Content
	if !strings.Contains(fullSide, "[2] Providers") || strings.Contains(fullSide, "[1] Status") {
		t.Fatalf("full side mode must show only the active block:\n%s", fullSide)
	}
	model.focus = focusInspector
	fullMain := model.View().Content
	if !strings.Contains(fullMain, "[0] Main") || strings.Contains(fullMain, "[2] Providers") {
		t.Fatalf("full main mode must show only the main context:\n%s", fullMain)
	}
}

func TestModelsOwnMostSideHeightAndEnterOpensInteractiveMainEditor(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.activePanel = int(dashboardModels)
	model.activeTabs[dashboardModels] = 1
	model.resize()
	heights := model.dashboardFrameHeights(33)
	for index, height := range heights {
		if index != int(dashboardModels) && heights[dashboardModels] <= height {
			t.Fatalf("Models height=%d must exceed block %d height=%d", heights[dashboardModels], index, height)
		}
	}
	model.setPaneItems(model.activeContextIndex(), []entry{{
		key: "qwen", title: "qwen", modelRef: "qwen", modelKind: "physical",
		payload: physicalModel{Name: "qwen", Policy: strategySpec{ID: "ordered-fallback"}, Enabled: true},
	}})
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.focus != focusInspector || model.mode != modeForm || model.form == nil || model.form.method != "physical_models.upsert" {
		t.Fatalf("Enter did not push Physical into an interactive Main editor: focus=%v mode=%v form=%#v", model.focus, model.mode, model.form)
	}
}

func TestSpaceSelectsWithinContextAndComboTabComposesSelectedPhysicalModels(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.activePanel = int(dashboardModels)
	model.activeTabs[dashboardModels] = 1
	model.resize()
	model.setPaneItems(model.activeContextIndex(), []entry{
		{key: "qwen-3.7-max", title: "qwen-3.7-max", modelRef: "qwen-3.7-max", modelKind: "physical", payload: physicalModel{Name: "qwen-3.7-max", Enabled: true}},
		{key: "deepseek-v4", title: "deepseek-v4", modelRef: "deepseek-v4", modelKind: "physical", payload: physicalModel{Name: "deepseek-v4", Enabled: true}},
	})
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if model.selectedCount() != 1 {
		t.Fatalf("Space selected %d rows, want 1", model.selectedCount())
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if model.mode != modeForm || model.form == nil {
		t.Fatal("c should open a model-composition editor from selected model entities")
	}
	if width := model.form.inputs[0].Width(); width < 20 {
		t.Fatalf("family name input width = %d; want a pane-width editor", width)
	}
	if got, want := model.form.memberLabels(), []string{"physical:qwen-3.7-max"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("preselected model members = %q, want %q", got, want)
	}
}

func TestTypedFormsBindMembersWithoutStringEncodedPersistence(t *testing.T) {
	physical := newPhysicalModelForm([]routeReference{{RouteID: "route-a"}, {RouteID: "route-b"}})
	physical.inputs[0].SetValue("qwen-3.7-max")
	params, err := physical.Params()
	if err != nil {
		t.Fatal(err)
	}
	if sources, ok := params["sources"].([]routeReference); !ok || len(sources) != 2 || sources[1].RouteID != "route-b" {
		t.Fatalf("physical sources are not typed: %#v", params["sources"])
	}
	combo := newComboModelForm([]modelReference{{Kind: "physical", ID: "qwen-3.7-max"}, {Kind: "combo", ID: "free"}})
	combo.inputs[0].SetValue("junior")
	params, err = combo.Params()
	if err != nil {
		t.Fatal(err)
	}
	if members, ok := params["members"].([]modelReference); !ok || len(members) != 2 || members[1].Kind != "combo" {
		t.Fatalf("combo members are not typed: %#v", params["members"])
	}
}

func TestModelTabsRetainCursorFilterAndSelectionIndependently(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 120, 40
	model.activePanel = int(dashboardModels)
	model.setPaneItems(model.contextIndex(int(dashboardModels), 0), []entry{{key: "route-a", title: "g4f/qwen-a"}, {key: "route-b", title: "orca/qwen-b"}})
	model.setPaneItems(model.contextIndex(int(dashboardModels), 1), []entry{{key: "physical-a", title: "qwen-a"}})
	discovered := &model.panels[model.contextIndex(int(dashboardModels), 0)]
	discovered.list.SetFilterText("g4f/")
	discovered.list.SetFilterState(list.FilterApplied)
	discovered.list.Select(1)
	discovered.selected["route-b"] = true
	_, _ = model.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if got := model.active().definition.view; got != "physical" {
		t.Fatalf("tab = %q, want physical", got)
	}
	_, _ = model.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	if model.active().list.FilterInput.Value() != "g4f/" || model.active().list.Index() != 1 || !model.active().selected["route-b"] {
		t.Fatalf("discovered context state was not retained: query=%q index=%d selected=%v", model.active().list.FilterInput.Value(), model.active().list.Index(), model.active().selected)
	}
	if model.panels[model.contextIndex(int(dashboardModels), 1)].selected["route-b"] {
		t.Fatal("selection leaked between separate management tabs")
	}
}

func TestModelWorkspaceUsesTypedViewsAndSeparatesPhysicalMapping(t *testing.T) {
	providers := []providerNode{{ID: "xkiro", Name: "XKiro", Prefix: "xkiro"}, {ID: "ocg", Name: "OCG", Prefix: "ocg"}}
	workspace, err := (modelWorkspaceClient{providers: providers}).Build(
		json.RawMessage(`[{"id":"r1","providerNodeId":"xkiro","providerPrefix":"xkiro","kind":"discovered","externalId":"qwen/qwen3.7-max:free"},{"id":"r2","providerNodeId":"ocg","providerPrefix":"ocg","kind":"discovered","externalId":"qwen3.7-max"}]`),
		json.RawMessage(`[{"name":"qwen-3.7-max","sources":[{"routeId":"r1"},{"routeId":"r2"}],"policy":{"id":"ordered-fallback"},"discoverable":false,"enabled":true}]`),
		json.RawMessage(`[{"name":"junior","strategy":{"id":"ordered-fallback"},"members":[{"kind":"physical","id":"qwen-3.7-max"}],"discoverable":true,"enabled":true}]`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Discovered) != 2 || len(workspace.Physical) != 1 || len(workspace.Combos) != 1 {
		t.Fatalf("unexpected typed workspace counts: discovered=%d physical=%d combos=%d", len(workspace.Discovered), len(workspace.Physical), len(workspace.Combos))
	}
	if workspace.Physical[0].title != "qwen-3.7-max" || !strings.Contains(workspace.Physical[0].detail, "xkiro/qwen/qwen3.7-max:free") {
		t.Fatalf("physical view lacks canonical identity or discovered routes: %#v", workspace.Physical[0])
	}
	if workspace.Combos[0].title != "junior" || !workspace.Combos[0].exposed {
		t.Fatalf("combo model or inline exposure missing: %#v", workspace.Combos[0])
	}
}

func TestSourcePickerBuildsFamilyFromProviderVariants(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.setPickerItems([]entry{
		{key: "route-xkiro", title: "xkiro/qwen/qwen3.7-max:free", routeRef: "xkiro/qwen/qwen3.7-max:free"},
		{key: "route-ocg", title: "ocg/qwen3.7-max", routeRef: "ocg/qwen3.7-max"},
		{key: "route-g4f", title: "g4f/Qwen:qwen3.7-max", routeRef: "g4f/Qwen:qwen3.7-max"},
	})
	model.mode = modeSourcePicker
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeySpace})
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeySpace})
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeySpace})
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.mode != modeForm || model.form == nil {
		t.Fatal("Enter should pass selected provider variants to the canonical model editor")
	}
	if got, want := strings.Join(model.form.comboMembers, ","), "xkiro/qwen/qwen3.7-max:free,ocg/qwen3.7-max,g4f/Qwen:qwen3.7-max"; got != want {
		t.Fatalf("model variants = %q, want %q", got, want)
	}
}
