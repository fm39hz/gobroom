package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
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

func TestSelectedConnectionTestShowsReadOnlyCatalogPreview(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 120, 40
	model.activePanel = int(dashboardConnections)
	model.activeTabs[dashboardConnections] = 1
	model.resize()
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "connection-2", title: "alternate", payload: connection{ID: "connection-2", ProviderNodeID: "provider-1", Name: "alternate", CredentialType: "api_key", Enabled: true}}})
	updated, command := model.updateDashboardAction(tea.KeyPressMsg{Code: 't', Text: "t"})
	if command == nil || updated.(*app).status != "testing selected connection's model-list endpoint (no import)…" {
		t.Fatalf("selected connection test did not start: model=%#v command=%v", updated, command)
	}
	model = updated.(*app)
	_, _ = model.Update(actionMsg{method: "connections.test", result: json.RawMessage(`{"connectionId":"connection-2","endpoint":"https://provider.test/v1/models","modelsFound":2,"preview":[{"id":"model-a"},{"id":"model-b"}],"previewLimit":100,"truncated":false}`)})
	if !strings.Contains(model.status, "2 models") || model.previewTitle != "Connection model-list test · no catalog changes" || !strings.Contains(model.mainPreview(*model.selectedEntry()), "model-a") {
		t.Fatalf("test result preview/status not shown: status=%q title=%q preview=%s", model.status, model.previewTitle, model.mainPreview(*model.selectedEntry()))
	}
	if model.refreshAfter("connections.test") != nil {
		t.Fatal("read-only connection test should not trigger model import or mutate pane data")
	}
}

func TestTUIDeviceAuthorizationShowsPublicInstructionsAndPollsDaemon(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 120, 40
	model.activePanel = int(dashboardConnections)
	model.activeTabs[dashboardConnections] = 1
	model.resize()
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "connection-oauth", title: "oauth account", payload: connection{ID: "connection-oauth", ProviderNodeID: "provider-oauth", Name: "oauth account", CredentialType: "oauth2", Enabled: true}}})
	updated, command := model.updateDashboardAction(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if command == nil || !strings.Contains(updated.(*app).status, "private state stays in daemon") {
		t.Fatalf("device authorization did not start from selected connection: status=%q", updated.(*app).status)
	}
	model = updated.(*app)
	pending := json.RawMessage(`{"sessionId":"auth-session-1","status":"awaiting_user","userCode":"ABCD-EFGH","verificationUrl":"https://identity.test/activate","expiresAt":"2026-10-07T13:00:00Z"}`)
	updatedModel, poll := model.Update(actionMsg{method: "auth.device.start", result: pending})
	model = updatedModel.(*app)
	if model.deviceAuthSessionID != "auth-session-1" || poll == nil || !strings.Contains(model.status, "ABCD-EFGH") || !strings.Contains(model.previewExtra, "identity.test/activate") || strings.Contains(model.previewExtra, "device_code") {
		t.Fatalf("device instructions/status are incomplete or private: status=%q preview=%q session=%q", model.status, model.previewExtra, model.deviceAuthSessionID)
	}
	completed := json.RawMessage(`{"sessionId":"auth-session-1","status":"completed","userCode":"ABCD-EFGH","verificationUrl":"https://identity.test/activate"}`)
	updatedModel, next := model.Update(actionMsg{method: "auth.device.get", result: completed})
	model = updatedModel.(*app)
	if next != nil || model.deviceAuthSessionID != "" || !strings.Contains(model.status, "credential saved") {
		t.Fatalf("completed device authorization was not rendered as terminal: status=%q session=%q", model.status, model.deviceAuthSessionID)
	}
}

func TestTUITestsSelectedConnectionAndShowsNonMutatingPreview(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 120, 40
	model.activePanel = int(dashboardConnections)
	model.activeTabs[dashboardConnections] = 1
	model.resize()
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "connection-2", title: "alternate", payload: connection{ID: "connection-2", ProviderNodeID: "provider-1", Name: "alternate", CredentialType: "api_key", Enabled: true}}})
	updated, command := model.updateDashboardAction(tea.KeyPressMsg{Code: 't', Text: "t"})
	if command == nil || updated.(*app).status != "testing selected connection's model-list endpoint (no import)…" {
		t.Fatalf("selected connection test did not start: model=%#v command=%v", updated, command)
	}
	model = updated.(*app)
	updated, _ = model.Update(actionMsg{method: "connections.test", result: json.RawMessage(`{"connectionId":"connection-2","endpoint":"https://provider.test/v1/models","modelsFound":2,"preview":[{"id":"model-a"},{"id":"model-b"}],"previewLimit":100,"truncated":false}`)})
	model = updated.(*app)
	if !strings.Contains(model.status, "2 models") || model.previewTitle != "Connection model-list test · no catalog changes" || !strings.Contains(model.mainPreview(*model.selectedEntry()), "model-a") {
		t.Fatalf("test result not shown in context: status=%q preview=%s", model.status, model.mainPreview(*model.selectedEntry()))
	}
}

func TestOAuthProviderConnectionFormDefaultsCredentialTypeFromAuthMode(t *testing.T) {
	form := newConnectionForm(providerNode{ID: "oauth-node", AuthMode: "oauth2"})
	for index, field := range form.fields {
		if field.key == "credentialType" {
			if got := form.inputs[index].Value(); got != "oauth2" {
				t.Fatalf("credential type default=%q", got)
			}
		}
		if field.key == "secret" && form.inputs[index].CharLimit < 4096 {
			t.Fatalf("OAuth secret field is too short for token JSON: limit=%d", form.inputs[index].CharLimit)
		}
	}
}

func TestConnectionFormUsesProviderAuthSetupSchema(t *testing.T) {
	catalog := []provider.DefinitionMetadata{{
		ID: "oauth-device", Auth: provider.AuthMetadata{ID: "device-oauth", SetupSchema: provider.SetupSchema{Fields: []provider.SetupField{
			{Name: "access_token", Type: "string", Required: true, Secret: true},
			{Name: "refresh_token", Type: "string", Secret: true},
			{Name: "expires_at", Type: "datetime"},
		}}},
	}}
	form := newConnectionFormWithMetadata(providerNode{ID: "node-oauth", DefinitionID: "oauth-device", AuthMode: "oauth2"}, catalog)
	for index, field := range form.fields {
		switch field.key {
		case "name":
			form.inputs[index].SetValue("work account")
		case "auth:access_token":
			if !field.secret {
				t.Fatal("auth schema secret marker was ignored")
			}
			form.inputs[index].SetValue("access-value")
		case "auth:refresh_token":
			form.inputs[index].SetValue("refresh-value")
		case "auth:expires_at":
			form.inputs[index].SetValue("2026-10-06T12:00:00Z")
		}
	}
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	var secret map[string]string
	if err := json.Unmarshal([]byte(params["secret"].(string)), &secret); err != nil {
		t.Fatal(err)
	}
	if secret["access_token"] != "access-value" || secret["refresh_token"] != "refresh-value" || secret["expires_at"] != "2026-10-06T12:00:00Z" {
		t.Fatalf("auth setup schema fields were not encoded into connection credential state: %#v", secret)
	}
	for index, field := range form.fields {
		if field.key == "auth:access_token" {
			form.inputs[index].SetValue("")
		}
	}
	if _, err := form.Params(); err == nil || !strings.Contains(err.Error(), "access_token is required") {
		t.Fatalf("required auth schema field was not validated: %v", err)
	}
}

func TestNoAuthConnectionFormAllowsBlankSecret(t *testing.T) {
	form := newConnectionForm(providerNode{ID: "public-node", AuthMode: "none"})
	for index, field := range form.fields {
		if field.key == "name" {
			form.inputs[index].SetValue("public")
		}
	}
	if _, err := form.Params(); err != nil {
		t.Fatalf("no-auth connection should not require a credential secret: %v", err)
	}
}

func TestStaticAuthConnectionFormRequiresSecret(t *testing.T) {
	form := newConnectionForm(providerNode{ID: "static-node", AuthMode: "api_key"})
	for index, field := range form.fields {
		if field.key == "name" {
			form.inputs[index].SetValue("personal")
		}
	}
	if _, err := form.Params(); err == nil {
		t.Fatal("static credential form should reject an empty secret")
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
		payload: physicalModel{Name: "qwen", Policy: strategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, Enabled: true},
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

func TestComboPickerFiltersViaSourcePrefixButShowsOneCanonicalPhysicalRow(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.activePanel = int(dashboardModels)
	model.activeTabs[dashboardModels] = 2
	model.providers = []providerNode{{ID: "node-orca", Name: "Orca", Prefix: "orca"}, {ID: "node-openrouter", Name: "OpenRouter", Prefix: "openrouter"}}
	model.raw = map[sectionID]json.RawMessage{
		sectionDiscovered: json.RawMessage(`[
{"id":"route-orca","providerNodeId":"node-orca","providerPrefix":"orca","kind":"discovered","externalId":"deepseek-v4-flash-free","displayName":"DeepSeek Free"},
{"id":"route-openrouter","providerNodeId":"node-openrouter","providerPrefix":"openrouter","kind":"discovered","externalId":"deepseek-v4-flash-0731:free","displayName":"DeepSeek Free"},
{"id":"route-other","providerNodeId":"node-openrouter","providerPrefix":"openrouter","kind":"discovered","externalId":"qwen-3.8-max","displayName":"Qwen"}]`),
		sectionPhysical: json.RawMessage(`[
{"name":"deepseek-v4-flash","sources":[{"routeId":"route-orca"},{"routeId":"route-openrouter"}],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true},
{"name":"qwen-3.8-max","sources":[{"routeId":"route-other"}],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true}]`),
		sectionComboModels: json.RawMessage(`[{"name":"junior","members":[],"strategy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true}]`),
	}
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "junior", title: "junior", payload: comboModel{Name: "junior", Enabled: true}}})
	if cmd := model.openTypedMemberPicker(); cmd != nil {
		t.Fatal("opening local Combo picker should not issue a daemon request")
	}
	model.picker.SetFilterText("orca/")
	model.picker.SetFilterState(list.FilterApplied)
	visible := model.picker.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("prefix filter produced %d candidate rows, want one canonical Physical row", len(visible))
	}
	item, ok := visible[0].(entry)
	if !ok || item.title != "deepseek-v4-flash" || item.modelKind != "physical" {
		t.Fatalf("prefix match should display the canonical Physical identity, got %#v", visible[0])
	}
	if !strings.Contains(item.detail, "orca/deepseek-v4-flash-free") {
		t.Fatalf("inspector should retain the source route that matched the query: %s", item.detail)
	}
}

func TestComboPickerPreservesOrderAndSupportsExplicitMemberReorder(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 140, 40
	model.activePanel = int(dashboardModels)
	model.activeTabs[dashboardModels] = 2
	model.raw = map[sectionID]json.RawMessage{
		sectionDiscovered: json.RawMessage(`[]`),
		sectionPhysical: json.RawMessage(`[
{"name":"alpha","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true},
{"name":"omega","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true},
{"name":"tau","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true}]`),
		sectionComboModels: json.RawMessage(`[]`),
	}
	original := comboModel{Name: "junior", Members: []modelReference{{Kind: "physical", ID: "omega", Weight: 3}, {Kind: "physical", ID: "alpha", Weight: 1}}, Strategy: strategySpec{Ref: kernel.StrategyRef("weighted-fallback", 1)}, Enabled: true}
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "junior", title: "junior", modelRef: "junior", modelKind: "combo", payload: original}})
	if cmd := model.openTypedMemberPicker(); cmd != nil {
		t.Fatal("opening local Combo picker unexpectedly issued a daemon request")
	}
	if got := model.comboMembersFromPicker(); !equalModelReferences(got, original.Members) {
		t.Fatalf("opening picker changed existing order: got %#v want %#v", got, original.Members)
	}
	model.memberPicker.SetFilterText("omega")
	model.memberPicker.SetFilterState(list.FilterApplied)
	// Add tau; additions append in explicit selection order.
	model.picker.Select(2)
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeySpace})
	want := []modelReference{{Kind: "physical", ID: "omega", Weight: 3}, {Kind: "physical", ID: "alpha", Weight: 1}, {Kind: "physical", ID: "tau", Weight: 1}}
	if got := model.comboMembersFromPicker(); !equalModelReferences(got, want) {
		t.Fatalf("new member did not append: got %#v want %#v", got, want)
	}
	if model.memberPicker.FilterInput.Value() != "omega" || model.memberPicker.FilterState() != list.FilterApplied {
		t.Fatalf("editing candidates reset independent member-pane filter: query=%q state=%v", model.memberPicker.FilterInput.Value(), model.memberPicker.FilterState())
	}
	if selected, ok := model.memberPicker.SelectedItem().(entry); !ok || selected.modelRef != "omega" {
		t.Fatalf("editing candidates moved independent member cursor: %#v", model.memberPicker.SelectedItem())
	}
	model.pickerFocus = 0
	model.memberPicker.Select(0)
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: '+', Text: "+"})
	want[0].Weight = 4
	if got := model.comboMembersFromPicker(); !equalModelReferences(got, want) {
		t.Fatalf("typed member weight edit got %#v want %#v", got, want)
	}
	model.memberPicker.SetFilterState(list.Unfiltered)
	model.memberPicker.SetFilterText("")
	// Candidate rows sort alphabetically, while the separate member pane owns
	// execution order. Explicit J moves alpha after tau.
	model.pickerFocus = 0
	model.memberPicker.Select(1)
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: 'J', Text: "J"})
	want = []modelReference{{Kind: "physical", ID: "omega", Weight: 4}, {Kind: "physical", ID: "tau", Weight: 1}, {Kind: "physical", ID: "alpha", Weight: 1}}
	if got := model.comboMembersFromPicker(); !equalModelReferences(got, want) {
		t.Fatalf("explicit member reorder got %#v want %#v", got, want)
	}
	model.picker.SetFilterText("omega")
	model.picker.SetFilterState(list.FilterApplied)
	if got := model.comboMembersFromPicker(); !equalModelReferences(got, want) {
		t.Fatalf("filtering changed ordered Combo membership: got %#v want %#v", got, want)
	}
	// Removing the focused selected member removes only that item.
	model.picker.SetFilterState(list.Unfiltered)
	model.pickerFocus = 1
	model.picker.Select(2) // tau in the alphabetically sorted candidate list
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeySpace})
	want = []modelReference{{Kind: "physical", ID: "omega", Weight: 4}, {Kind: "physical", ID: "alpha", Weight: 1}}
	if got := model.comboMembersFromPicker(); !equalModelReferences(got, want) {
		t.Fatalf("removing member changed unrelated order: got %#v want %#v", got, want)
	}
	requests := make(chan daemon.IPCRequest, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := t.TempDir() + "/control.sock"
	server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		requests <- request
		return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{}}
	})
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	model.ipcPath = socket
	_, command := model.updateSourcePickerKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("ctrl+s did not submit Combo membership to the daemon")
	}
	_ = command()
	request := <-requests
	if request.Method != "combo_models.upsert" {
		t.Fatalf("save method = %q, want combo_models.upsert", request.Method)
	}
	gotMembers, ok := request.Params["members"].([]any)
	if !ok || len(gotMembers) != len(want) {
		t.Fatalf("saved members have wrong shape/length: %#v", request.Params["members"])
	}
	for index, expected := range want {
		member, ok := gotMembers[index].(map[string]any)
		if !ok || member["kind"] != expected.Kind || member["id"] != expected.ID || member["weight"] != float64(expected.Weight) {
			t.Fatalf("saved member[%d]=%#v, want %#v", index, gotMembers[index], expected)
		}
	}
	strategy, ok := request.Params["strategy"].(map[string]any)
	if !ok || strategy["ref"].(map[string]any)["id"] != "weighted-fallback" || strategy["ref"].(map[string]any)["contractVersion"] != float64(1) {
		t.Fatalf("strategy primitive was not preserved in save request: %#v", request.Params["strategy"])
	}
	if config, ok := strategy["config"].(map[string]any); ok && len(config) != 0 {
		t.Fatalf("weighted strategy unexpectedly received primitive options: %#v", config)
	}
}

func TestComboPickerUsesIndependentPanesAndLazyGitNavigation(t *testing.T) {
	raw := map[sectionID]json.RawMessage{
		sectionDiscovered: json.RawMessage(`[]`),
		sectionPhysical: json.RawMessage(`[
{"name":"alpha","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true},
{"name":"omega","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true}]`),
		sectionComboModels: json.RawMessage(`[]`),
	}
	value := comboModel{Name: "junior", Members: []modelReference{{Kind: "physical", ID: "omega"}}, Enabled: true}
	var model *app
	for _, width := range []int{140, 88, 60} {
		model = newApp("/tmp/gobroom.sock")
		model.width, model.height = width, 40
		model.activePanel = int(dashboardModels)
		model.activeTabs[dashboardModels] = 2
		model.raw = raw
		model.setPaneItems(model.activeContextIndex(), []entry{{key: "junior", title: "junior", payload: value}})
		if cmd := model.openTypedMemberPicker(); cmd != nil {
			t.Fatal("local Combo editor unexpectedly called daemon")
		}
		view := model.render()
		if !strings.Contains(view, "Ordered members") || !strings.Contains(view, "Physical / Combo candidates") || !strings.Contains(view, "omega") || !strings.Contains(view, "alpha") {
			t.Fatalf("width %d: Combo editor did not render both independent panes:\n%s", width, view)
		}
		if got := lipgloss.Width(view); got > width {
			t.Fatalf("width %d: Combo editor rendered %d cells", width, got)
		}
	}
	if model.pickerFocus != 1 {
		t.Fatalf("candidate pane should receive initial focus, got %d", model.pickerFocus)
	}
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if model.pickerFocus != 0 {
		t.Fatal("h did not focus ordered members pane")
	}
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if model.pickerFocus != 1 {
		t.Fatal("l did not focus candidate pane")
	}
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if model.picker.Index() != 1 {
		t.Fatalf("j did not move within the focused candidate pane: index=%d", model.picker.Index())
	}
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.pickerPreview == "" {
		t.Fatal("Enter did not inspect the focused candidate")
	}
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if model.mode != modeSourcePicker || model.pickerPreview != "" {
		t.Fatal("first Esc should close details but preserve editor context")
	}
	_, _ = model.updateSourcePickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if model.mode != modeBrowse {
		t.Fatal("second Esc should cancel the unsaved editor")
	}
}

func TestTUIToggleComboExposurePreservesStrategyAndMemberPolicy(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.activePanel = int(dashboardModels)
	model.activeTabs[dashboardModels] = 2
	value := comboModel{
		Name: "junior", Members: []modelReference{{Kind: "physical", ID: "qwen", Weight: 3}},
		Strategy: strategySpec{Ref: kernel.StrategyRef("round-robin-fallback", 1), Config: map[string]any{"stickyLimit": 2}}, Enabled: true,
	}
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "junior", title: "junior", modelRef: "junior", modelKind: "combo", payload: value}})
	requests := make(chan daemon.IPCRequest, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := t.TempDir() + "/control.sock"
	server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		requests <- request
		return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{}}
	})
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	model.ipcPath = socket
	command := model.toggleExposure()
	if command == nil {
		t.Fatal("Combo exposure action did not call daemon")
	}
	_ = command()
	request := <-requests
	if request.Method != "combo_models.upsert" || request.Params["discoverable"] != true {
		t.Fatalf("exposure request=%#v", request)
	}
	strategy, ok := request.Params["strategy"].(map[string]any)
	if !ok || strategy["ref"].(map[string]any)["id"] != value.Strategy.Ref.ID || strategy["ref"].(map[string]any)["contractVersion"] != float64(value.Strategy.Ref.ContractVersion) {
		t.Fatalf("strategy was lost while exposing Combo: %#v", request.Params["strategy"])
	}
	config, ok := strategy["config"].(map[string]any)
	if !ok || config["stickyLimit"] != float64(2) {
		t.Fatalf("strategy options were lost while exposing Combo: %#v", strategy["config"])
	}
	members, ok := request.Params["members"].([]any)
	if !ok || len(members) != 1 {
		t.Fatalf("member edge options were lost while exposing Combo: %#v", request.Params["members"])
	}
	member, ok := members[0].(map[string]any)
	if !ok || member["weight"] != float64(3) {
		t.Fatalf("member edge options were lost while exposing Combo: %#v", members[0])
	}
}

func TestComboEditorSaveAndExposureReachOpenAIModelsProjection(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/combo-flow.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"glm-5.3", "qwen-3.7-max"} {
		if err := db.UpsertPhysicalModel(store.PhysicalModel{Name: name, Policy: store.StrategySpec{Ref: kernel.StrategyRef("ordered-fallback", 1)}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	initial := store.ComboModel{Name: "junior", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "qwen-3.7-max", Weight: 3}}, Strategy: store.StrategySpec{Ref: kernel.StrategyRef("weighted-fallback", 1)}, Enabled: true}
	if err := db.UpsertComboModel(initial); err != nil {
		t.Fatal(err)
	}
	server := api.NewServer(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan daemon.IPCRequest, 4)
	socket := t.TempDir() + "/control.sock"
	ipc := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		requests <- request
		if request.Method != "combo_models.upsert" {
			return daemon.IPCResponse{ID: request.ID, OK: false, Error: "unexpected method " + request.Method}
		}
		data, err := json.Marshal(request.Params)
		if err != nil {
			return daemon.IPCResponse{ID: request.ID, OK: false, Error: err.Error()}
		}
		var combo store.ComboModel
		if err := json.Unmarshal(data, &combo); err != nil {
			return daemon.IPCResponse{ID: request.ID, OK: false, Error: err.Error()}
		}
		if err := db.UpsertComboModel(combo); err != nil {
			return daemon.IPCResponse{ID: request.ID, OK: false, Error: err.Error()}
		}
		if err := server.Control().Reload(); err != nil {
			return daemon.IPCResponse{ID: request.ID, OK: false, Error: err.Error()}
		}
		return daemon.IPCResponse{ID: request.ID, OK: true, Result: combo}
	})
	if err := ipc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer ipc.Close()

	model := newApp(socket)
	model.width, model.height = 120, 40
	model.activePanel = int(dashboardModels)
	model.activeTabs[dashboardModels] = 2
	model.raw = map[sectionID]json.RawMessage{
		sectionDiscovered:  json.RawMessage(`[]`),
		sectionPhysical:    json.RawMessage(`[ {"name":"glm-5.3","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true}, {"name":"qwen-3.7-max","sources":[],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"enabled":true} ]`),
		sectionComboModels: json.RawMessage(`[{"name":"junior","members":[{"kind":"physical","id":"qwen-3.7-max","weight":3}],"strategy":{"ref":{"kind":"strategy","id":"weighted-fallback","contractVersion":1}},"enabled":true}]`),
	}
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "junior", title: "junior", modelRef: "junior", modelKind: "combo", payload: comboModel{Name: "junior", Members: []modelReference{{Kind: "physical", ID: "qwen-3.7-max", Weight: 3}}, Strategy: strategySpec{Ref: kernel.StrategyRef("weighted-fallback", 1)}, Enabled: true}}})
	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	model = updated.(*app)
	if cmd != nil || model.mode != modeSourcePicker {
		t.Fatal("opening Combo editor unexpectedly called daemon")
	}
	model.picker.Select(0) // glm-5.3, displayed once as a canonical candidate
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	_, save := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if save == nil {
		t.Fatal("Combo editor did not submit its ordered member graph")
	}
	updated, _ = model.Update(save())
	model = updated.(*app)
	firstRequest := <-requests
	if firstRequest.Params["discoverable"] != false {
		t.Fatalf("saving membership unexpectedly exposed Combo: %#v", firstRequest.Params)
	}
	saved, err := db.ComboModel("junior")
	if err != nil || len(saved.Members) != 2 || saved.Members[0].ID != "qwen-3.7-max" || saved.Members[1].ID != "glm-5.3" || saved.Members[0].Weight != 3 || saved.Members[1].Weight != 1 {
		t.Fatalf("saved ordered Combo=%#v err=%v", saved, err)
	}
	var savedTUI comboModel
	savedJSON, _ := json.Marshal(saved)
	if err := json.Unmarshal(savedJSON, &savedTUI); err != nil {
		t.Fatal(err)
	}
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "junior", title: "junior", modelRef: "junior", modelKind: "combo", payload: savedTUI}})
	_, expose := model.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if expose == nil {
		t.Fatal("focused Combo exposure action did not submit")
	}
	updated, _ = model.Update(expose())
	model = updated.(*app)
	secondRequest := <-requests
	if secondRequest.Params["discoverable"] != true {
		t.Fatalf("exposure request did not enable Combo: %#v", secondRequest.Params)
	}
	recorder := httptest.NewRecorder()
	server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /v1/models status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var models struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 1 || models.Data[0]["id"] != "junior" {
		t.Fatalf("exposed Combo projection=%#v, want only junior", models.Data)
	}
}

func TestSelectedConnectionModelImportReviewsAndSubmitsOnlyCheckedIDs(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 120, 40
	model.activePanel = int(dashboardConnections)
	model.activeTabs[dashboardConnections] = 1
	model.setPaneItems(model.activeContextIndex(), []entry{{key: "conn-a", title: "account A", payload: connection{ID: "conn-a", ProviderNodeID: "node-a", Name: "account A", Enabled: true}}})
	requests := make(chan daemon.IPCRequest, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := t.TempDir() + "/control.sock"
	server := daemon.NewIPCServer(socket, func(_ context.Context, request daemon.IPCRequest) daemon.IPCResponse {
		requests <- request
		if request.Method == "connections.preview_models" {
			return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{
				"providerNodeId": "node-a", "connectionId": "conn-a", "endpoint": "https://provider.test/v1/models",
				"modelsFound": 3, "previewLimit": 5000, "complete": true,
				"models": []map[string]any{{"id": "model-a", "displayName": "A"}, {"id": "model-b", "displayName": "B"}, {"id": "model-c", "displayName": "C"}},
			}}
		}
		return daemon.IPCResponse{ID: request.ID, OK: true, Result: map[string]any{"models": 2, "available": 3, "complete": true}}
	})
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	model.ipcPath = socket
	updated, previewCommand := model.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	model = updated.(*app)
	if previewCommand == nil {
		t.Fatal("i did not request a read-only catalog preview")
	}
	updated, _ = model.Update(previewCommand())
	model = updated.(*app)
	if model.mode != modeModelImport || len(model.importPreview.Models) != 3 || len(model.importSelected) != 3 {
		t.Fatalf("preview did not open selectable import review: mode=%v preview=%#v selected=%#v", model.mode, model.importPreview, model.importSelected)
	}
	if item, ok := model.importPicker.Items()[0].(entry); !ok || !strings.Contains(item.title, "new") {
		t.Fatalf("review rows must visibly distinguish new/already-imported entries: %#v", model.importPicker.Items()[0])
	}
	model.importPicker.Select(1) // deselect model-b, retain the other checked IDs
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	updated, importCommand := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(*app)
	if importCommand == nil {
		t.Fatal("Enter did not import the reviewed selection")
	}
	_ = importCommand()
	previewRequest := <-requests
	importRequest := <-requests
	if previewRequest.Method != "connections.preview_models" || importRequest.Method != "providers.refresh_models" {
		t.Fatalf("request sequence=%q then %q", previewRequest.Method, importRequest.Method)
	}
	if importRequest.Params["nodeID"] != "node-a" || importRequest.Params["connectionID"] != "conn-a" {
		t.Fatalf("selected connection context lost: %#v", importRequest.Params)
	}
	selectedIDs, ok := importRequest.Params["modelIDs"].([]any)
	if !ok || len(selectedIDs) != 2 || selectedIDs[0] != "model-a" || selectedIDs[1] != "model-c" {
		t.Fatalf("import did not submit exact checked upstream IDs: %#v", importRequest.Params["modelIDs"])
	}
	updated, previewCommand = model.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	model = updated.(*app)
	updated, _ = model.Update(previewCommand())
	model = updated.(*app)
	_, _ = model.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	updated, entitlementsCommand := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(*app)
	if entitlementsCommand == nil {
		t.Fatal("empty selection should still allow applying account-entitlement evidence")
	}
	_ = entitlementsCommand()
	secondPreview, entitlementApply := <-requests, <-requests
	if secondPreview.Method != "connections.preview_models" {
		t.Fatalf("second review method=%q", secondPreview.Method)
	}
	if entitlementApply.Method != "providers.refresh_models" {
		t.Fatalf("entitlement-only method=%q", entitlementApply.Method)
	}
	modelIDs, ok := entitlementApply.Params["modelIDs"].([]any)
	if !ok || len(modelIDs) != 0 {
		t.Fatalf("empty selection should be explicit entitlements-only apply: %#v", entitlementApply.Params["modelIDs"])
	}
}

func equalModelReferences(a, b []modelReference) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
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

func TestComboEditFormRoundTripsPrimitiveOptionsAndMemberWeight(t *testing.T) {
	value := comboModel{
		Name:         "junior",
		Members:      []modelReference{{Kind: "physical", ID: "qwen", Weight: 4}},
		Strategy:     strategySpec{Ref: kernel.StrategyRef("round-robin-fallback", 1), Config: map[string]any{"stickyLimit": 3}},
		Discoverable: true, Enabled: true,
	}
	selected := entry{payload: value}
	form := newResourceForm(0, &selected, "")
	if form == nil {
		t.Fatal("Combo edit form was not created")
	}
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	strategy, ok := params["strategy"].(strategySpec)
	if !ok || strategy.Ref != value.Strategy.Ref || strategy.Config["stickyLimit"] != float64(3) {
		t.Fatalf("strategy primitive/options were not preserved: %#v", params["strategy"])
	}
	members, ok := params["members"].([]modelReference)
	if !ok || len(members) != 1 || members[0].Weight != 4 {
		t.Fatalf("typed member weight was not preserved: %#v", params["members"])
	}
	for index, field := range form.fields {
		if field.key == "strategyOptions" {
			form.inputs[index].SetValue("[")
			if _, err := form.Params(); err == nil {
				t.Fatal("invalid strategy options JSON should be rejected before save")
			}
			return
		}
	}
	t.Fatal("strategy options JSON field is missing from Combo editor")
}

func TestProviderCreateFormLetsDaemonDeriveProtocolDefinition(t *testing.T) {
	form := newResourceForm(int(sectionProviders), nil, "")
	if got := form.providerPreset; got != "openai" || form.inputs[0].Value() != "OpenAI" {
		t.Fatalf("provider form did not start with OpenAI preset: preset=%q name=%q", got, form.inputs[0].Value())
	}
	form.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if form.providerPreset != "anthropic" || form.inputs[0].Value() != "Anthropic" || form.inputs[2].Value() != "https://api.anthropic.com/v1" {
		t.Fatalf("Ctrl+P did not apply Anthropic preset: preset=%q name=%q url=%q", form.providerPreset, form.inputs[0].Value(), form.inputs[2].Value())
	}
	params, err := form.Params()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := params["definitionID"]; exists {
		t.Fatalf("provider form should not pin OpenAI definition when protocol is changed: %#v", params)
	}
	if params["protocol"] != "anthropic" {
		t.Fatalf("protocol=%#v", params["protocol"])
	}
	form.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if form.providerPreset != "custom" || form.inputs[2].Value() != "" {
		t.Fatalf("Ctrl+P did not switch to an empty custom provider form: preset=%q url=%q", form.providerPreset, form.inputs[2].Value())
	}
}

func TestComboFormOpensSearchableStrategyCatalogAndAppliesSchemaDefaults(t *testing.T) {
	model := newApp("/tmp/gobroom.sock")
	model.width, model.height = 120, 40
	model.strategyCatalog = kernel.StrategyDefinitions()
	model.rebuildStrategyPicker()
	model.form = newComboModelForm(nil)
	model.form.inputs[0].SetValue("junior")
	model.form.focus(1)
	model.mode = modeForm
	updated, _ := model.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(*app)
	if model.mode != modeStrategyPicker {
		t.Fatalf("Enter on strategy field opened mode %v, want strategy picker", model.mode)
	}
	for _, width := range []int{140, 88, 60} {
		model.width = width
		model.resize()
		view := model.render()
		if got := lipgloss.Width(view); got > width {
			t.Fatalf("strategy picker width %d rendered %d cells", width, got)
		}
		if !strings.Contains(view, "Strategy catalog") || !strings.Contains(view, "Primitive contract") {
			t.Fatalf("strategy picker omitted catalog/contract panes at width %d:\n%s", width, view)
		}
	}
	model.strategyPicker.SetFilterText("stickyLimit")
	visible := model.strategyPicker.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("strategy search returned %d rows, want one sticky strategy: %#v", len(visible), visible)
	}
	row, ok := visible[0].(entry)
	if !ok || row.key != "strategy:round-robin-fallback@1" || !strings.Contains(row.detail, "stickyLimit") {
		t.Fatalf("search result lacks canonical strategy/options contract: %#v", visible[0])
	}
	updated, _ = model.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(*app)
	if model.mode != modeForm || model.form.inputs[1].Value() != "round-robin-fallback" || model.form.inputs[2].Value() != `{"stickyLimit":1}` {
		t.Fatalf("strategy choice/defaults not applied: mode=%v strategy=%q options=%q", model.mode, model.form.inputs[1].Value(), model.form.inputs[2].Value())
	}
	params, err := model.form.Params()
	if err != nil {
		t.Fatal(err)
	}
	strategy := params["strategy"].(strategySpec)
	if strategy.Ref != kernel.StrategyRef("round-robin-fallback", 1) || strategy.Config["stickyLimit"] != float64(1) {
		t.Fatalf("selected primitive did not produce validated typed config: %#v", strategy)
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
		json.RawMessage(`[{"name":"qwen-3.7-max","sources":[{"routeId":"r1"},{"routeId":"r2"}],"policy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"discoverable":false,"enabled":true}]`),
		json.RawMessage(`[{"name":"junior","strategy":{"ref":{"kind":"strategy","id":"ordered-fallback","contractVersion":1}},"members":[{"kind":"physical","id":"qwen-3.7-max"}],"discoverable":true,"enabled":true}]`),
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
		{key: "route-xkiro", title: "xkiro/qwen/qwen3.7-max:free", routeRef: "xkiro/qwen/qwen3.7-max:free", payload: discoveredRoute{ID: "xkiro-r", ProviderPrefix: "xkiro", ExternalID: "qwen/qwen3.7-max:free"}},
		{key: "route-ocg", title: "ocg/qwen3.7-max", routeRef: "ocg/qwen3.7-max", payload: discoveredRoute{ID: "ocg-r", ProviderPrefix: "ocg", ExternalID: "qwen3.7-max"}},
		{key: "route-g4f", title: "g4f/Qwen:qwen3.7-max", routeRef: "g4f/Qwen:qwen3.7-max", payload: discoveredRoute{ID: "g4f-r", ProviderPrefix: "g4f", ExternalID: "Qwen:qwen3.7-max"}},
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
	if len(model.form.typedSources) != 3 || model.form.typedSources[0].Fidelity != "alias" || len(model.form.typedSources[0].Evidence) == 0 {
		t.Fatalf("typed source references=%#v", model.form.typedSources)
	}
}
