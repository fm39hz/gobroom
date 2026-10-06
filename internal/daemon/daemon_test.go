package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/auth"
	"github.com/fm39hz/gobroom/internal/discovery"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
	"github.com/fm39hz/gobroom/internal/store"
)

func TestIPCControlCRUDUsesDaemonServices(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := &Daemon{store: s, server: api.NewServer(s)}
	response := d.handleIPC(nil, IPCRequest{ID: "1", Method: "providers.create", Params: map[string]any{"name": "G4F", "prefix": "g4f", "baseUrl": "https://example.test/v1", "protocol": "openai_chat"}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "2", Method: "providers.list"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	items, ok := response.Result.([]store.ProviderNode)
	if !ok || len(items) != 1 {
		t.Fatalf("providers=%#v", response.Result)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "3", Method: "strategies.list"})
	definitions, ok := response.Result.([]kernel.StrategyDefinition)
	if !response.OK || !ok || len(definitions) == 0 {
		t.Fatalf("strategy catalog=%#v error=%q", response.Result, response.Error)
	}
	var hasStickyLimit bool
	for _, definition := range definitions {
		if definition.ID == "round-robin-fallback" && len(definition.Options) == 1 && definition.Options[0].Key == "stickyLimit" {
			hasStickyLimit = true
		}
	}
	if !hasStickyLimit {
		t.Fatal("strategy catalog omitted round-robin-fallback's stickyLimit contract")
	}
}

func TestComboIPCExposureProjectsOnlySelectedNameToOpenAIModels(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/combo-exposure.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := api.NewServer(s)
	d := &Daemon{store: s, server: server}
	params := map[string]any{
		"name": "junior", "members": []any{},
		"strategy":     map[string]any{"id": "ordered-fallback", "config": map[string]any{}},
		"discoverable": false, "enabled": true,
	}
	response := d.handleIPC(context.Background(), IPCRequest{ID: "combo-hidden", Method: "combo_models.upsert", Params: params})
	if !response.OK {
		t.Fatalf("create hidden Combo: %s", response.Error)
	}
	getModels := func() []map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /v1/models status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var result struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	if models := getModels(); len(models) != 0 {
		t.Fatalf("hidden Combo leaked into /v1/models: %#v", models)
	}
	params["discoverable"] = true
	response = d.handleIPC(context.Background(), IPCRequest{ID: "combo-exposed", Method: "combo_models.upsert", Params: params})
	if !response.OK {
		t.Fatalf("expose Combo: %s", response.Error)
	}
	models := getModels()
	if len(models) != 1 || models[0]["id"] != "junior" {
		t.Fatalf("/v1/models projection=%#v, want only junior", models)
	}
}

func TestPrefixValidationUsesProtocolDefaultDefinition(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/prefix.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := (&Daemon{store: s}).validatePrefix("gemini", "", "gemini"); err != nil {
		t.Fatalf("Gemini protocol's default definition should permit its built-in prefix: %v", err)
	}
}

func TestProviderCreationDefaultsConnectionAuthFromDefinition(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/provider-auth.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := &Daemon{
		store: s, server: api.NewServer(s),
		providerAuthModes:   map[string]string{"oauth-provider": "oauth2"},
		providerAuthFlowIDs: map[string]string{"oauth-provider": "oauth-provider:oauth2"},
		authFlows: map[string]provider.AuthFlow{
			"oauth-provider:oauth2": provider.OAuthAuth{Config: auth.OAuthConfig{ClientID: "client", AuthURL: "https://oauth.test/authorize", TokenURL: "https://oauth.test/token"}},
		},
	}
	response := d.handleIPC(nil, IPCRequest{ID: "provider", Method: "providers.create", Params: map[string]any{
		"name": "OAuth provider", "prefix": "oauth-test", "baseUrl": "https://provider.test/v1",
		"protocol": "openai_chat", "definitionID": "oauth-provider",
	}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	node, ok := response.Result.(store.ProviderNode)
	if !ok || node.AuthMode != "oauth2" {
		t.Fatalf("provider defaults=%#v", response.Result)
	}
	response = d.handleIPC(nil, IPCRequest{ID: "connection", Method: "connections.create", Params: map[string]any{
		"providerNodeID": node.ID, "name": "account", "credentialType": node.AuthMode, "secret": `{"access_token":"access","refresh_token":"refresh"}`,
	}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	connections, err := s.Connections(node.ID)
	if err != nil || len(connections) != 1 || connections[0].CredentialType != "oauth2" {
		t.Fatalf("connections=%#v err=%v", connections, err)
	}
}

func TestRefreshedCredentialPersistenceRetainsOAuthClientSecret(t *testing.T) {
	credential := kernel.Credential{Secret: "new-access", RefreshToken: "new-refresh", ClientID: "client", ClientSecret: "secret", ExpiresAt: time.Now().Add(time.Hour)}
	stored, err := refreshedCredentialSecret(credential)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(stored), &state); err != nil {
		t.Fatal(err)
	}
	if state["access_token"] != credential.Secret || state["refresh_token"] != credential.RefreshToken || state["client_id"] != credential.ClientID || state["client_secret"] != credential.ClientSecret {
		t.Fatalf("persisted OAuth state=%#v", state)
	}
}

func TestCredentialRefreshLocksAreScopedPerConnection(t *testing.T) {
	d := &Daemon{}
	releaseFirst := d.lockCredentialRefresh("account-a")
	defer releaseFirst()
	acquiredOther := make(chan struct{})
	go func() {
		release := d.lockCredentialRefresh("account-b")
		close(acquiredOther)
		release()
	}()
	select {
	case <-acquiredOther:
	case <-time.After(time.Second):
		t.Fatal("refresh for another connection was blocked by the first connection's lock")
	}
}

func TestDaemonPersistsManifestClassifiedQuotaEvidenceAcrossRestart(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable in test environment: %v", err)
	}
	reset := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"catalog-only-model"}]}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"fault":{"reason":"PLAN_DRAINED","explanation":"weekly budget exhausted","reopens_at":"` + reset.Format(time.RFC3339) + `","period":"weekly","limit":50,"consumed":50,"remaining":0}}`))
	}))
	upstream.Listener = listener
	upstream.Start()
	defer upstream.Close()

	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{
		ID: "quota-fixture", Version: "1", DisplayName: "Quota fixture",
		Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "static-secret"},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json"},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http"},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "openai-chat-json"},
			ResponseCodec:   provider.PrimitiveRef{Kind: provider.PrimitiveResponseCodec, ID: "openai-sse"},
			ErrorClassifier: provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json"},
			ErrorClassifierOptions: provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
				QuotaScope: kernel.ScopeConnection,
				CodePath:   "/fault/reason", MessagePath: "/fault/explanation", ResetAtPath: "/fault/reopens_at",
				WindowNamePath: "/fault/period", WindowLimitPath: "/fault/limit", WindowUsedPath: "/fault/consumed", WindowRemainingPath: "/fault/remaining",
				QuotaWindowKind: "requests", QuotaCodes: []string{"PLAN_DRAINED"}, QuotaMessageTokens: []string{"budget exhausted"},
			}},
		}, provider.OperationModels: {
			Endpoint:    provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json"},
			Transport:   provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http"},
			ModelSource: provider.PrimitiveRef{Kind: provider.PrimitiveModelSource, ID: "openai-models"},
		}},
	}
	manifest, err := provider.EncodeProviderDefinitionJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "quota-fixture.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "gobroom.db")
	state, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	node, err := state.CreateProviderNode(store.CreateProviderNodeInput{Name: "Quota fixture", Prefix: "quota-fixture", BaseURL: upstream.URL + "/v1", Protocol: "openai_chat", DefinitionID: definition.ID})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := state.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "api_key", Secret: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.UpsertCatalogModel(store.UpsertCatalogModelInput{ID: "quota-route", ProviderNodeID: node.ID, Kind: "custom", ExternalID: "quota-model", DisplayName: "Quota model"}); err != nil {
		t.Fatal(err)
	}
	if err := state.UpsertPhysicalModel(store.PhysicalModel{Name: "quota-physical", Sources: []store.RouteReference{{RouteID: "quota-route", Fidelity: kernel.FidelityExact}}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.UpsertComboModel(store.ComboModel{Name: "quota-role", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: "quota-physical"}}, Discoverable: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	config := Config{DBPath: dbPath, IPCPath: filepath.Join(root, "run", "gobroom.sock"), ProviderManifestDir: manifestDir}
	first := New(config)
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	connTest := first.handleIPC(context.Background(), IPCRequest{ID: "test-connection", Method: "connections.test", Params: map[string]any{"connectionID": connection.ID}})
	if !connTest.OK {
		t.Fatalf("connection test failed: %s", connTest.Error)
	}
	preview, ok := connTest.Result.(discovery.ConnectionTestResult)
	if !ok || preview.ConnectionID != connection.ID || preview.ModelsFound != 1 || len(preview.Preview) != 1 || preview.Preview[0].ID != "catalog-only-model" {
		t.Fatalf("connection test preview=%#v", connTest.Result)
	}
	modelPreview := first.handleIPC(context.Background(), IPCRequest{ID: "preview-models", Method: "connections.preview_models", Params: map[string]any{"connectionID": connection.ID}})
	if !modelPreview.OK {
		t.Fatalf("connection model review failed: %s", modelPreview.Error)
	}
	fullPreview, ok := modelPreview.Result.(discovery.ConnectionModelsPreview)
	if !ok || fullPreview.ConnectionID != connection.ID || fullPreview.ModelsFound != 1 || len(fullPreview.Models) != 1 || fullPreview.Models[0].ID != "catalog-only-model" {
		t.Fatalf("full connection preview=%#v", modelPreview.Result)
	}
	catalog, err := first.store.Models()
	if err != nil || len(catalog) != 1 {
		t.Fatalf("read-only connection test changed catalog: models=%#v err=%v", catalog, err)
	}
	importResult := first.handleIPC(context.Background(), IPCRequest{ID: "refresh-connection", Method: "providers.refresh_models", Params: map[string]any{"nodeID": node.ID, "connectionID": connection.ID}})
	if !importResult.OK {
		t.Fatalf("selected-connection model refresh failed: %s", importResult.Error)
	}
	catalog, err = first.store.Models()
	if err != nil || len(catalog) != 2 {
		t.Fatalf("selected-connection refresh did not import model catalog: models=%#v err=%v", catalog, err)
	}
	request, err := normalize.Map("/v1/chat/completions", http.Header{}, map[string]any{
		"model": "quota-role", "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": "please answer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	responseWriter := httptest.NewRecorder()
	if err := first.kernel.Execute(context.Background(), request.Request, kernel.Credential{}, responseWriter); err == nil || !strings.Contains(err.Error(), "no usable route") {
		t.Fatalf("expected classified upstream quota failure, got err=%v body=%s", err, responseWriter.Body.String())
	}
	var saved quota.Snapshot
	var sawHeaderWindow bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshots, loadErr := first.store.QuotaSnapshots()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, snapshot := range snapshots {
			if snapshot.WindowName == "weekly" {
				saved = snapshot
			}
			if snapshot.WindowName == "rate_limit" {
				sawHeaderWindow = true
			}
		}
		if saved.WindowName != "" && sawHeaderWindow {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if saved.Remaining == nil || *saved.Remaining != 0 || saved.ResetAt == nil || !saved.ResetAt.Equal(reset) || saved.WindowName != "weekly" || !sawHeaderWindow {
		t.Fatalf("daemon did not persist configured quota evidence: %#v", saved)
	}
	if err := first.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := New(config)
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer second.Stop(context.Background())
	var route kernel.Route
	for _, candidate := range second.kernel.Snapshots.Load().Routes {
		if candidate.ExternalModel == "quota-model" && candidate.CredentialID == connection.ID {
			route = candidate
			break
		}
	}
	if route.ID == "" {
		t.Fatal("restart did not rebuild the provider route")
	}
	healthState := second.policy.Health.Snapshot()[route.ID]
	if healthState.LastCause != kernel.CauseQuotaExhausted || healthState.LastScope != kernel.ScopeConnection {
		t.Fatalf("passive health cause/scope was not restored: %#v", healthState)
	}
	if second.policy.Usable(route, time.Now()) {
		t.Fatal("persisted exhausted quota should block the route after daemon restart")
	}
	if !second.policy.Usable(route, reset.Add(time.Second)) {
		t.Fatal("persisted route should become eligible after reset without health polling")
	}
}
