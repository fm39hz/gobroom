package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fm39hz/gobroom/internal/daemon"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

const (
	setupIntegrationDefinitionID = "tui-setup-integration-openai"
	setupIntegrationAuthID       = "tui-setup-integration-auth"
	setupIntegrationSession      = "private-flow-state"
)

var (
	setupIntegrationInputRef = extensions.Ref{Kind: extensions.SchemaKind, ID: "tui.setup.input", ContractVersion: 1}
	setupIntegrationAuthRef  = extensions.Ref{Kind: extensions.SchemaKind, ID: "tui.setup.auth-options", ContractVersion: 1}
)

type setupIntegrationAuth struct{}

func (setupIntegrationAuth) ID() string { return setupIntegrationAuthID }
func (setupIntegrationAuth) SetupSchema() provider.SetupSchema {
	return provider.SetupSchema{Fields: []provider.SetupField{{Name: "secret", Type: "string", Required: true, Secret: true}}}
}
func (setupIntegrationAuth) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	if input.Secret == "" {
		return kernel.Credential{}, errors.New("credential is empty")
	}
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (setupIntegrationAuth) Refresh(_ context.Context, _ provider.AuthInput, credential kernel.Credential) (provider.CredentialState, error) {
	return provider.NewCredentialState(credential.Secret)
}

type setupIntegrationFlow struct {
	mu   sync.Mutex
	seen [][]byte
}

func (*setupIntegrationFlow) ID() string { return setupIntegrationAuthID }
func (*setupIntegrationFlow) SetupSchema() provider.SetupSchema {
	return setupIntegrationAuth{}.SetupSchema()
}
func (*setupIntegrationFlow) Resolve(ctx context.Context, input provider.AuthInput) (kernel.Credential, error) {
	return setupIntegrationAuth{}.Resolve(ctx, input)
}
func (*setupIntegrationFlow) Refresh(ctx context.Context, input provider.AuthInput, credential kernel.Credential) (provider.CredentialState, error) {
	return setupIntegrationAuth{}.Refresh(ctx, input, credential)
}
func setupIntegrationAction() provider.AuthorizationAction {
	ref := setupIntegrationInputRef
	return provider.AuthorizationAction{ID: "continue", Kind: provider.AuthorizationInput, Label: "Enter setup value", InputSchemaRef: &ref}
}

type setupIntegrationPrivate struct {
	Private   string    `json:"private"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (f *setupIntegrationFlow) BeginAuthorization(context.Context, provider.AuthInput) (provider.AuthorizationTransition, error) {
	private := setupIntegrationPrivate{Private: setupIntegrationSession, ExpiresAt: time.Now().Add(time.Minute)}
	encoded, err := json.Marshal(private)
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	state, err := provider.NewAuthorizationState(string(encoded))
	return provider.AuthorizationTransition{State: state, Actions: []provider.AuthorizationAction{setupIntegrationAction()}, ExpiresAt: private.ExpiresAt}, err
}
func (f *setupIntegrationFlow) AdvanceAuthorization(_ context.Context, state provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
	if state.Encoded() == "" || event.Kind != provider.AuthorizationSubmit || event.ActionID != "continue" {
		return provider.AuthorizationTransition{}, provider.AuthorizationInputRejected{}
	}
	f.mu.Lock()
	f.seen = append(f.seen, append([]byte(nil), event.Value...))
	seenCount := len(f.seen)
	f.mu.Unlock()
	var input struct {
		Counter json.Number `json:"counter"`
		Secret  string      `json:"secret"`
	}
	if json.Unmarshal(event.Value, &input) != nil {
		return provider.AuthorizationTransition{}, provider.AuthorizationInputRejected{}
	}
	if input.Secret == "reject-once" {
		return provider.AuthorizationTransition{}, provider.AuthorizationInputRejected{}
	}
	if seenCount == 1 {
		var private setupIntegrationPrivate
		if err := json.Unmarshal([]byte(state.Encoded()), &private); err != nil {
			return provider.AuthorizationTransition{}, err
		}
		private.Private = "next-step"
		encoded, err := json.Marshal(private)
		if err != nil {
			return provider.AuthorizationTransition{}, err
		}
		next, err := provider.NewAuthorizationState(string(encoded))
		return provider.AuthorizationTransition{State: next, Actions: []provider.AuthorizationAction{setupIntegrationAction()}, ExpiresAt: private.ExpiresAt}, err
	}
	credential, err := provider.NewCredentialState("final-access-token")
	if err != nil {
		return provider.AuthorizationTransition{}, err
	}
	return provider.AuthorizationTransition{Credential: &credential}, nil
}

func (f *setupIntegrationFlow) observedInputs() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([][]byte, len(f.seen))
	for i, value := range f.seen {
		result[i] = append([]byte(nil), value...)
	}
	return result
}

func cloneOpenAIcompatible(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-chat", 1))
	if !ok {
		t.Fatal("generic OpenAI-compatible chat definition was not found")
	}
	definition.ID, definition.Version, definition.DisplayName = setupIntegrationDefinitionID, "1", "TUI setup integration"
	definition.DefaultCredentialType = "interactive"
	definition.AuthModes = []provider.AuthModeDefinition{{CredentialType: "interactive", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: setupIntegrationAuthID, ContractVersion: 1}, Presentation: kernel.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}}
	return definition
}

func setupIntegrationModule(t *testing.T, flow *setupIntegrationFlow) provider.RuntimeModule {
	t.Helper()
	definition := cloneOpenAIcompatible(t)
	return provider.RuntimeModuleFunc{Name: "tui.setup.integration", Version: "test-1", HostContractVersion: provider.RuntimeModuleHostContractVersion, Init: func(runtime *provider.RuntimeRegistry) error {
		document, err := extensions.BindSchemaDocument(setupIntegrationAuthRef, json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`))
		if err != nil {
			return err
		}
		if err := runtime.RegisterAuthFactoryWithOptionsSchema(setupIntegrationAuthID, 1, setupIntegrationAuthRef, document, func(provider.AuthOptions) (provider.AuthFlow, error) {
			return flow, nil
		}); err != nil {
			return err
		}
		if err := runtime.RegisterSchemaDocument(setupIntegrationInputRef, json.RawMessage(`{"type":"object","properties":{"counter":{"type":"integer"},"secret":{"type":"string","writeOnly":true,"minLength":1}},"required":["counter","secret"],"additionalProperties":false}`)); err != nil {
			return err
		}
		return runtime.Primitives.RegisterDefinition(definition)
	}}
}

func setupIntegrationCall(t *testing.T, path, method string, params map[string]any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := daemon.CallIPC(ctx, path, daemon.IPCRequest{Method: method, Params: params})
	if err != nil {
		t.Fatalf("IPC %s: %v", method, err)
	}
	return mustSetupJSON(t, response.Result)
}

func mustSetupJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func setupIntegrationCommand(t *testing.T, command tea.Cmd) tea.Msg {
	t.Helper()
	if command == nil {
		t.Fatal("expected app command")
	}
	return command()
}

func setupIntegrationUpdate(t *testing.T, model *app, command tea.Cmd) *app {
	t.Helper()
	for step := 0; command != nil && step < 5; step++ {
		message := setupIntegrationCommand(t, command)
		updated, next := model.Update(message)
		result, ok := updated.(*app)
		if !ok {
			t.Fatalf("Update returned %T, want *app", updated)
		}
		model = result
		// Follow only immediate schema fetches and form compilation. The other
		// commands returned here are timer polls, which this test never drives.
		if !model.pendingSetupInput && !model.setupFormPending && !model.setupNeedsRefresh {
			break
		}
		command = next
	}
	return model
}

func awaitSetupActivation(t *testing.T, model *app) *app {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for model.setupSessionID != "" {
		select {
		case <-ticker.C:
			command := model.invokeSetup("auth.setup.get", map[string]any{"sessionID": model.setupSessionID})
			rawMessage := setupIntegrationCommand(t, command)
			message, ok := rawMessage.(setupResultMsg)
			if !ok {
				t.Fatalf("auth.setup.get returned %T", rawMessage)
			}
			updated, _ := model.Update(message) // discard the normal timer poll command
			var valid bool
			model, valid = updated.(*app)
			if !valid {
				t.Fatalf("setup activation Update returned %T, want *app", updated)
			}
		case <-timeout.C:
			t.Fatalf("credential activation did not settle; setup view=%+v status=%q", model.setupView, model.status)
		}
	}
	return model
}

func TestSetupAuthorizationE2EOverUnixIPC(t *testing.T) {
	var inferenceCalls atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inferenceCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer final-access-token" {
			http.Error(w, "unexpected inference request", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"authenticated"},"finish_reason":"stop"}]}`))
	}))
	defer httpServer.Close()

	temp := t.TempDir()
	flow := &setupIntegrationFlow{}
	config := daemon.Config{DBPath: filepath.Join(temp, "setup.db"), RuntimeSpoolPath: filepath.Join(temp, "spool.json"), IPCPath: filepath.Join(temp, "setup.sock"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	d := daemon.NewWithProviderModules(config, setupIntegrationModule(t, flow))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })

	var node store.ProviderNode
	if err := json.Unmarshal(setupIntegrationCall(t, config.IPCPath, "providers.create", map[string]any{"name": "TUI setup", "prefix": "tuisetup", "baseURL": httpServer.URL + "/v1", "protocol": "openai_chat", "definitionID": setupIntegrationDefinitionID, "definitionContractVersion": 1, "authMode": "interactive"}), &node); err != nil {
		t.Fatal(err)
	}
	var connection store.ConnectionRecord
	if err := json.Unmarshal(setupIntegrationCall(t, config.IPCPath, "connections.create", map[string]any{"providerNodeID": node.ID, "name": "Interactive account", "credentialType": "interactive", "secret": "seed-credential"}), &connection); err != nil {
		t.Fatal(err)
	}

	model := newApp(config.IPCPath)
	model.width, model.height = 120, 40
	model.activePanel, model.activeTabs[1] = 1, 1 // Providers block, Connections tab.
	model.resize()
	model = setupIntegrationUpdate(t, model, model.fetch(sectionProviders))
	model = setupIntegrationUpdate(t, model, model.fetch(sectionConnections))
	if len(model.active().items) != 1 || model.selectedEntry() == nil {
		t.Fatalf("connection was not available to the TUI: %+v", model.active().items)
	}
	_, command := model.updateKey(tea.KeyPressMsg{Code: 'D'})
	model = setupIntegrationUpdate(t, model, command)
	if model.setupSessionID == "" || model.setupView.Status != "awaiting_user" || model.form == nil || model.form.setup == nil {
		t.Fatalf("D did not start setup and install the real schema form: status=%q session=%q form=%#v", model.status, model.setupSessionID, model.form)
	}
	if len(model.raw[sectionExtensionCatalog]) == 0 {
		t.Fatal("setup form did not obtain the registered runtime catalog")
	}

	const secret = "900719925474099312345"
	model.form.inputs[0].SetValue(secret)
	model.form.inputs[1].SetValue("sealed-first-step")
	for _, public := range []string{model.form.inputs[0].View(), model.form.inputs[1].View(), model.status, model.previewExtra, model.lastError} {
		if strings.Contains(public, secret) {
			t.Fatalf("secret appeared in public TUI state: %q", public)
		}
	}
	submittedForm := model.form
	_, command = model.updateKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if submittedForm != nil {
		for _, input := range submittedForm.inputs {
			if input.Value() != "" {
				t.Fatal("old form retained submitted input")
			}
		}
	}
	if model.form != nil {
		t.Fatal("submit retained the setup form and its input")
	}
	if command == nil {
		t.Fatal("Ctrl+s did not schedule auth.setup.advance")
	}
	var firstAdvance setupResultMsg
	advanceMessage := setupIntegrationCommand(t, command)
	if message, ok := advanceMessage.(setupResultMsg); ok {
		firstAdvance = message
	} else {
		t.Fatalf("advance command returned %T", advanceMessage)
	}
	seen := flow.observedInputs()
	if len(seen) != 1 || string(seen[0]) != `{"counter":`+secret+`,"secret":"sealed-first-step"}` {
		t.Fatalf("provider got typed values %#v; want exact JSON big integer payload", seen)
	}
	model = setupIntegrationUpdate(t, model, func() tea.Msg { return firstAdvance })
	if model.setupView.Revision != 2 || model.form == nil || model.form.setup == nil {
		t.Fatalf("next action did not return to a fresh input form: view=%+v form=%#v", model.setupView, model.form)
	}

	// A recoverable provider rejection leaves the challenge active and requires
	// a fresh, explicit submit; it never replays the rejected value itself.
	model.form.inputs[0].SetValue("42")
	model.form.inputs[1].SetValue("reject-once")
	_, command = model.updateKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = setupIntegrationUpdate(t, model, command)
	if model.setupView.Status != "awaiting_user" || model.setupView.Revision != 2 || model.form != nil {
		t.Fatalf("recoverable rejection did not preserve challenge and clear editor: status=%+v form=%#v", model.setupView, model.form)
	}
	seen = flow.observedInputs()
	if len(seen) != 2 || string(seen[1]) != `{"counter":42,"secret":"reject-once"}` {
		t.Fatalf("rejected value call count/payload = %d/%s", len(seen), seen[1])
	}
	_, command = model.updateKey(tea.KeyPressMsg{Code: 'D'})
	if command == nil {
		t.Fatal("D did not reopen the recoverable challenge")
	}
	model = setupIntegrationUpdate(t, model, command)
	if model.form == nil || model.form.inputs[0].Value() != "" || model.form.inputs[1].Value() != "" {
		t.Fatalf("reentered challenge retained rejected input: form=%#v", model.form)
	}
	model.form.inputs[0].SetValue("43")
	model.form.inputs[1].SetValue("otp-final-value")
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		// A second frontend can finish the session after the TUI editor closes.
		_, _ = model.updateKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		payload, err := json.Marshal(struct {
			Counter json.Number `json:"counter"`
			Secret  string      `json:"secret"`
		}{Counter: json.Number("43"), Secret: "otp-final-value"})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(temp, "final-input.json")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		cliCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		output, err := exec.CommandContext(cliCtx, cli, "--ipc", config.IPCPath, "connections", "setup", "submit", "--session-id", model.setupSessionID, "--action-id", "continue", "--revision", strconv.FormatUint(model.setupView.Revision, 10), "--input-file", path).CombinedOutput()
		if err != nil {
			t.Fatalf("installed CLI submit: %v %s", err, output)
		}
		if strings.Contains(string(output), "otp-final-value") || strings.Contains(string(output), "sealed-first-step") {
			t.Fatal("installed CLI echoed submitted input")
		}
		model = setupIntegrationUpdate(t, model, model.invokeSetup("auth.setup.get", map[string]any{"sessionID": model.setupSessionID}))
	} else {
		_, command = model.updateKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		model = setupIntegrationUpdate(t, model, command)
	}
	if model.setupView.Status != "completed" {
		t.Fatalf("authorization did not complete: status=%q view=%+v", model.status, model.setupView)
	}
	model = awaitSetupActivation(t, model)
	seen = flow.observedInputs()
	if len(seen) != 3 || string(seen[2]) != `{"counter":43,"secret":"otp-final-value"}` {
		t.Fatalf("final driver input was not exact: %q", seen[2])
	}

	// The credential produced by the flow must be activated and used by the
	// normal inference path, proving persistence and provider binding.
	probe := setupIntegrationCall(t, config.IPCPath, "connections.test_inference", map[string]any{"connectionID": connection.ID, "modelID": "tuisetup:test-model"})
	if len(probe) == 0 {
		t.Fatalf("authenticated inference failed: %s", probe)
	}
	if inferenceCalls.Load() == 0 {
		t.Fatal("inference did not reach the fake upstream")
	}
}

func TestSetupAuthorizationEscResumeAndCtrlXCancelOverUnixIPC(t *testing.T) {
	temp := t.TempDir()
	flow := &setupIntegrationFlow{}
	config := daemon.Config{DBPath: filepath.Join(temp, "setup.db"), RuntimeSpoolPath: filepath.Join(temp, "spool.json"), IPCPath: filepath.Join(temp, "setup.sock")}
	d := daemon.NewWithProviderModules(config, setupIntegrationModule(t, flow))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	var node store.ProviderNode
	if err := json.Unmarshal(setupIntegrationCall(t, config.IPCPath, "providers.create", map[string]any{"name": "TUI cancel", "prefix": "tuicancel", "baseURL": "https://unused.invalid/v1", "protocol": "openai_chat", "definitionID": setupIntegrationDefinitionID, "definitionContractVersion": 1, "authMode": "interactive"}), &node); err != nil {
		t.Fatal(err)
	}
	var connection store.ConnectionRecord
	if err := json.Unmarshal(setupIntegrationCall(t, config.IPCPath, "connections.create", map[string]any{"providerNodeID": node.ID, "name": "Interactive account", "credentialType": "interactive", "secret": "seed-credential"}), &connection); err != nil {
		t.Fatal(err)
	}
	model := newApp(config.IPCPath)
	model.width, model.height = 100, 32
	model.activePanel, model.activeTabs[1] = 1, 1
	model.resize()
	model = setupIntegrationUpdate(t, model, model.fetch(sectionProviders))
	model = setupIntegrationUpdate(t, model, model.fetch(sectionConnections))
	_, command := model.updateKey(tea.KeyPressMsg{Code: 'D'})
	model = setupIntegrationUpdate(t, model, command)
	oldForm := model.form
	_, _ = model.updateKey(tea.KeyPressMsg{Code: tea.KeyEsc}) // discard the public-status poll tick
	if model.form != nil || model.setupSessionID == "" {
		t.Fatalf("Esc did not close only the editor: form=%#v session=%q", model.form, model.setupSessionID)
	}
	for _, input := range oldForm.inputs {
		if input.Value() != "" {
			t.Fatal("Esc left its old secret buffer populated")
		}
	}
	_, command = model.updateKey(tea.KeyPressMsg{Code: 'D'})
	if command == nil {
		t.Fatal("D resume did not schedule asynchronous setup form compilation")
	}
	model = setupIntegrationUpdate(t, model, command)
	if model.form == nil || model.form.setup == nil {
		t.Fatal("D did not resume the existing authorization challenge")
	}
	model.form.inputs[1].SetValue("cancelled-secret")
	_, command = model.updateKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if model.form != nil || model.setupSessionID == "" || command == nil {
		t.Fatal("Ctrl+x did not wipe editor and issue auth.setup.cancel")
	}
	epoch := model.setupEpoch
	model = setupIntegrationUpdate(t, model, command)
	if model.setupSessionID != "" || model.setupView.SessionID != "" {
		t.Fatalf("daemon did not cancel setup session: session=%q view=%+v", model.setupSessionID, model.setupView)
	}
	updated, _ := model.Update(setupResultMsg{epoch: epoch - 1, sessionID: "stale-session", method: "auth.setup.get", result: json.RawMessage(`{"sessionId":"stale-session","connectionId":"x","status":"awaiting_user","revision":99}`)})
	model = updated.(*app)
	if model.setupSessionID != "" || strings.Contains(model.previewExtra, "stale") {
		t.Fatal(fmt.Sprintf("stale setup reply changed cancelled UI state: session=%q preview=%q", model.setupSessionID, model.previewExtra))
	}
}
