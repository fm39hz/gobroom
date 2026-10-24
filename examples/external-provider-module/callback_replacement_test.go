package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const replacementCallbackAuthID = "example-callback-replacement-auth"

type replacementCallbackBarrier struct {
	entered, release, returned chan struct{}
	enterOnce, releaseOnce     sync.Once
	advances                   atomic.Int32
}

type replacementCallbackModule struct {
	port        int
	correlation *atomic.Value
	barrier     *replacementCallbackBarrier
}

func (replacementCallbackModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.callback-replacement", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}
func (m replacementCallbackModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterSchemaDocument(callbackInputSchemaRef, json.RawMessage(callbackInputSchemaDocument)); err != nil {
		return err
	}
	return runtime.RegisterAuthFactory(replacementCallbackAuthID, func(provider.AuthOptions) (provider.AuthFlow, error) {
		return replacementCallbackAuth{callbackFixtureAuth: callbackFixtureAuth{port: m.port, correlation: m.correlation}, barrier: m.barrier}, nil
	})
}

// The decorator reuses the real fixture callback protocol and credential flow.
// It deliberately returns valid success late, even if its context is cancelled.
type replacementCallbackAuth struct {
	callbackFixtureAuth
	barrier *replacementCallbackBarrier
}

func (replacementCallbackAuth) ID() string { return replacementCallbackAuthID }
func (a replacementCallbackAuth) AdvanceAuthorization(ctx context.Context, state provider.AuthorizationState, event provider.AuthorizationEvent) (provider.AuthorizationTransition, error) {
	a.barrier.advances.Add(1)
	result, err := a.callbackFixtureAuth.AdvanceAuthorization(ctx, state, event)
	if err != nil || result.Credential == nil {
		return result, err
	}
	a.barrier.enterOnce.Do(func() { close(a.barrier.entered) })
	<-a.barrier.release
	close(a.barrier.returned)
	return result, nil
}

type callbackHTTPResult struct {
	status int
	err    error
}
type callbackReplacementFixture struct {
	host                    *portabilityHost
	connectionID, sessionID string
	callbackURL             *url.URL
	correlation             string
	barrier                 *replacementCallbackBarrier
	callbackDone            chan callbackHTTPResult
	calls                   atomic.Int32
}

func newCallbackReplacementFixture(t *testing.T) *callbackReplacementFixture {
	t.Helper()
	f := &callbackReplacementFixture{barrier: &replacementCallbackBarrier{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}, callbackDone: make(chan callbackHTTPResult, 1)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer manual-callback-key" {
			t.Errorf("replacement was not serving: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "callback-race-model" {
			t.Error("wrong imported model request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"callback-race","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"replacement served"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	root, err := os.MkdirTemp("", "gobroom-callback-race-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	manifests := filepath.Join(root, "providers")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := callbackFixtureManifest(t)
	definition.AuthModes[0].Auth.ID = replacementCallbackAuthID
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifests, "callback.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	var correlation atomic.Value
	module := replacementCallbackModule{port: port, correlation: &correlation, barrier: f.barrier}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifests, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPToken: "client-key-4b7e"}
	f.host = &portabilityHost{config: config, modules: []provider.RuntimeModule{module}, service: daemon.NewWithProviderModules(config, module)}
	t.Cleanup(func() {
		f.barrier.releaseOnce.Do(func() { close(f.barrier.release) })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := f.host.service.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := f.host.service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	create := func(method string, params map[string]any) string {
		var row struct{ ID string }
		if err := json.Unmarshal(portabilityOK(t, config.IPCPath, method, params), &row); err != nil || row.ID == "" {
			t.Fatalf("%s id=%q err=%v", method, row.ID, err)
		}
		return row.ID
	}
	node := create("providers.create", map[string]any{"name": "Callback replacement", "prefix": "callbackrace", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat", "definitionID": callbackProviderID})
	original, err := json.Marshal(opaqueCredentialDocument{Ticket: "original-callback-key", Subject: "original-callback-subject", Workspace: "workspace-7"})
	if err != nil {
		t.Fatal(err)
	}
	f.connectionID = create("connections.create", map[string]any{"providerNodeID": node, "name": "Race account", "credentialType": "opaque_ticket", "secret": string(original)})
	portabilityOK(t, config.IPCPath, "custom_models.upsert", map[string]any{"id": "callback-race-route", "providerNodeID": node, "kind": "custom", "externalID": "callback-race-model", "displayName": "Race model", "connectionIds": []string{f.connectionID}})
	portabilityOK(t, config.IPCPath, "physical_models.upsert", map[string]any{"name": "callback-race-physical", "identity": map[string]any{"canonicalName": "callback-race-model"}, "sources": []map[string]any{{"routeId": "callback-race-route", "fidelity": "exact"}}, "enabled": true})
	portabilityOK(t, config.IPCPath, "combo_models.upsert", map[string]any{"name": "callback-race-role", "members": []map[string]any{{"kind": "physical", "id": "callback-race-physical"}}, "discoverable": true, "enabled": true})
	view := decodeCallbackView(t, portabilityOK(t, config.IPCPath, "auth.setup.start", map[string]any{"connectionID": f.connectionID}))
	if view.SessionID == "" || view.Status != "awaiting_user" || len(view.CallbackModes) != 1 || view.CallbackModes[0].Mode != daemon.AuthorizationCallbackLoopback {
		t.Fatalf("callback grant not ready: %#v", view)
	}
	f.sessionID = view.SessionID
	f.correlation = correlation.Load().(string)
	f.callbackURL = &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/identity/return"}
	f.callbackURL.RawQuery = url.Values{"tenant": {"external-module"}, "ticket": {f.correlation}, "proof": {"late-callback-proof"}}.Encode()
	go func() {
		response, err := (&http.Client{Timeout: 15 * time.Second}).Get(f.callbackURL.String())
		result := callbackHTTPResult{err: err}
		if response != nil {
			result.status = response.StatusCode
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		f.callbackDone <- result
	}()
	select {
	case <-f.barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider callback never entered held completion")
	}
	return f
}

func (f *callbackReplacementFixture) replace(t *testing.T, cli string) {
	t.Helper()
	secret, err := json.Marshal(opaqueCredentialDocument{Ticket: "manual-callback-key", Subject: "manual-callback-subject", Workspace: "workspace-7"})
	if err != nil {
		t.Fatal(err)
	}
	if cli != "" {
		portabilityCLI(t, cli, f.host.config.IPCPath, nil, "connections", "update", "--id", f.connectionID, "--secret", string(secret))
	} else {
		portabilityOK(t, f.host.config.IPCPath, "connections.update", map[string]any{"id": f.connectionID, "secret": string(secret)})
	}
	select {
	case <-f.barrier.release:
		t.Fatal("replacement was not completed while provider work was held")
	default:
	}
}

func (f *callbackReplacementFixture) serve(t *testing.T) {
	t.Helper()
	before := f.calls.Load()
	status, body := sg1Post(t, f.host, "/v1/chat/completions", json.RawMessage(`{"model":"callback-race-role","messages":[{"role":"user","content":"hello"}]}`), "")
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || status != 200 || len(result.Choices) != 1 || result.Choices[0].Message.Content != "replacement served" || f.calls.Load() != before+1 {
		t.Fatalf("replacement serving failed: HTTP%d body=%s err=%v", status, body, err)
	}
}

func (f *callbackReplacementFixture) terminal(t *testing.T) daemon.AuthorizationSetupView {
	t.Helper()
	return decodeCallbackView(t, portabilityOK(t, f.host.config.IPCPath, "auth.setup.get", map[string]any{"sessionID": f.sessionID}))
}

func (f *callbackReplacementFixture) finish(t *testing.T) {
	t.Helper()
	f.barrier.releaseOnce.Do(func() { close(f.barrier.release) })
	select {
	case result := <-f.callbackDone:
		if result.err != nil || result.status != 400 {
			t.Fatalf("stale callback receipt status=%d transportFailed=%v", result.status, result.err != nil)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stale callback did not finish")
	}
	select {
	case <-f.barrier.returned:
	default:
		t.Fatal("callback receipt preceded provider completion")
	}
	view := f.terminal(t)
	if view.Status != "failed" || len(view.Actions) != 0 || len(view.CallbackModes) != 0 || view.Activation != nil {
		t.Fatalf("stale callback retained authority: %#v", view)
	}
	if f.barrier.advances.Load() != 1 {
		t.Fatal("callback unexpectedly replayed provider authorization")
	}
	listener, err := net.Listen("tcp", f.callbackURL.Host)
	if err != nil {
		t.Fatalf("callback listener not released: %v", err)
	}
	_ = listener.Close()
}

func (f *callbackReplacementFixture) restart(t *testing.T) { t.Helper(); f.host.restart(t) }

func TestPublicCallbackReplacementCannotCommitStaleCredential(t *testing.T) {
	f := newCallbackReplacementFixture(t)
	f.replace(t, "")
	f.serve(t)
	f.finish(t)
	f.serve(t)
	assertCallbackReplacementPrivacy(t, f,
		portabilityOK(t, f.host.config.IPCPath, "auth.setup.get", map[string]any{"sessionID": f.sessionID}),
		portabilityOK(t, f.host.config.IPCPath, "status", nil),
		portabilityOK(t, f.host.config.IPCPath, "logs.list", map[string]any{"limit": 100}))
	response, err := (&http.Client{Timeout: time.Second}).Get(f.callbackURL.String())
	if response != nil {
		_ = response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("stale callback replay accepted")
		}
	}
	_ = err
	if f.barrier.advances.Load() != 1 {
		t.Fatal("replay entered authorization driver")
	}
	f.restart(t)
	f.serve(t)
}
