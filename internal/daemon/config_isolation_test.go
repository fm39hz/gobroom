package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/controlplane"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

type configIsolationTransform struct{}

func (configIsolationTransform) Definition() kernel.TransformDefinition {
	options := extensions.Ref{Kind: extensions.SchemaKind, ID: "fixture.config-isolation-options", ContractVersion: 1}
	return kernel.TransformDefinition{
		Ref:                   extensions.Ref{Kind: kernel.RequestTransformKind, ID: "fixture.config-isolation", ContractVersion: 1},
		ImplementationVersion: "test-1", Label: "Config isolation", Description: "Test-only schema-bound transform.",
		OptionsSchemaRef: &options, Stage: kernel.TransformBeforeRequirements, Effects: []kernel.TransformEffect{kernel.TransformInput},
	}
}

func (configIsolationTransform) Apply(context.Context, *kernel.NormalizedRequest, json.RawMessage) error {
	return nil
}

func TestConfigApplyRejectionsPreserveServingAndInflightRequests(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(releaseFirst)
		}
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"data":[{"id":"isolation-model"}]}`)
			return
		}
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		call := calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(writer, "data: {\"id\":\"isolation\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"prior-route\"},\"finish_reason\":null}]}\n\n")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if call == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-request.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(writer, "data: {\"id\":\"isolation\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	module := provider.RuntimeModuleFunc{
		Name: "fixture.config-isolation", Version: "test-1", HostContractVersion: provider.RuntimeModuleHostContractVersion,
		Init: func(runtime *provider.RuntimeRegistry) error {
			return runtime.RegisterRequestTransform(configIsolationTransform{}, json.RawMessage(`{"type":"object","properties":{"label":{"type":"string","minLength":1}},"required":["label"],"additionalProperties":false}`))
		},
	}
	root := t.TempDir()
	config := Config{DBPath: filepath.Join(root, "daemon.db"), RuntimeSpoolPath: filepath.Join(root, "spool.json"), IPCPath: filepath.Join(root, "run", "daemon.sock"), ArtifactStoreDir: filepath.Join(root, "artifacts")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewWithProviderModules(config, module)
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Stop(context.Background()) }()
	defer release()

	cli := os.Getenv("GOBROOM_TEST_CLI")
	runConfigCLI := func(method string, params map[string]any) ([]byte, error) {
		t.Helper()
		commandCtx, cancelCommand := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCommand()
		command := exec.CommandContext(commandCtx, cli, "--ipc", config.IPCPath, "config-"+strings.TrimPrefix(method, "config."))
		if params != nil {
			encoded, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			command.Stdin = bytes.NewReader(encoded)
		}
		return command.CombinedOutput()
	}
	callIPC := func(method string, params map[string]any) IPCResponse {
		t.Helper()
		if cli != "" && (method == "config.export" || method == "config.apply") {
			data, err := runConfigCLI(method, params)
			if err != nil {
				t.Fatalf("installed CLI %s: %v (%s)", method, err, data)
			}
			return IPCResponse{OK: true, Result: json.RawMessage(data)}
		}
		callCtx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCall()
		response, err := CallIPC(callCtx, config.IPCPath, IPCRequest{ID: method, Method: method, Params: params})
		if err != nil {
			t.Fatalf("IPC %s: %v", method, err)
		}
		return response
	}
	callIPCExpectedFailure := func(method string, params map[string]any) (IPCResponse, error) {
		t.Helper()
		if cli != "" {
			data, err := runConfigCLI(method, params)
			return IPCResponse{OK: err == nil, Error: string(data)}, err
		}
		callCtx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCall()
		return CallIPC(callCtx, config.IPCPath, IPCRequest{ID: method, Method: method, Params: params})
	}
	paramsFor := func(value any) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var params map[string]any
		if err := json.Unmarshal(encoded, &params); err != nil {
			t.Fatal(err)
		}
		return params
	}
	readBundle := func() controlplane.ConfigBundle {
		t.Helper()
		response := callIPC("config.export", nil)
		encoded, err := json.Marshal(response.Result)
		if !response.OK || err != nil {
			t.Fatalf("config.export result=%#v err=%v", response.Result, err)
		}
		var bundle controlplane.ConfigBundle
		if err := json.Unmarshal(encoded, &bundle); err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	statusVersions := func() (uint64, uint64) {
		t.Helper()
		response := callIPC("status", nil)
		encoded, err := json.Marshal(response.Result)
		if !response.OK || err != nil {
			t.Fatalf("status result=%#v err=%v", response.Result, err)
		}
		var status struct {
			Control uint64 `json:"controlSnapshotVersion"`
			Serving uint64 `json:"servingSnapshotVersion"`
		}
		if err := json.Unmarshal(encoded, &status); err != nil {
			t.Fatal(err)
		}
		return status.Control, status.Serving
	}
	assertUnchanged := func(before controlplane.ConfigBundle, control, serving uint64, rejection string) {
		t.Helper()
		after := readBundle()
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("%s changed exported config\nbefore=%#v\nafter=%#v", rejection, before, after)
		}
		gotControl, gotServing := statusVersions()
		if gotControl != control || gotServing != serving {
			t.Fatalf("%s changed snapshot versions: control/serving=%d/%d, want %d/%d", rejection, gotControl, gotServing, control, serving)
		}
	}

	createdProvider := callIPC("providers.create", map[string]any{"name": "Isolation provider", "prefix": "isolation", "baseUrl": upstream.URL + "/v1", "protocol": "openai_chat"})
	if !createdProvider.OK {
		t.Fatalf("create provider: %s", createdProvider.Error)
	}
	providerResult, ok := createdProvider.Result.(map[string]any)
	if !ok {
		t.Fatalf("provider result=%#v", createdProvider.Result)
	}
	var node store.ProviderNode
	if err := decodeParams(providerResult, &node); err != nil {
		t.Fatal(err)
	}
	createdConnection := callIPC("connections.create", map[string]any{"providerNodeID": node.ID, "name": "isolation account", "credentialType": "api_key", "secret": "test-secret"})
	if !createdConnection.OK {
		t.Fatalf("create connection: %s", createdConnection.Error)
	}
	connectionResult, ok := createdConnection.Result.(map[string]any)
	if !ok {
		t.Fatalf("connection result=%#v", createdConnection.Result)
	}
	var connection store.ConnectionRecord
	if err := decodeParams(connectionResult, &connection); err != nil {
		t.Fatal(err)
	}
	if refreshed := callIPC("providers.refresh_models", map[string]any{"nodeID": node.ID, "connectionID": connection.ID}); !refreshed.OK {
		t.Fatalf("discover provider model: %s", refreshed.Error)
	}
	routes, err := d.store.DiscoveredRoutes(node.ID)
	if err != nil || len(routes) != 1 {
		t.Fatalf("discovered routes=%#v err=%v", routes, err)
	}
	physical := store.PhysicalModel{Name: "isolation-physical", Identity: kernel.PhysicalIdentity{CanonicalName: "isolation-model"}, Sources: []store.RouteReference{{RouteID: routes[0].ID, Fidelity: kernel.FidelityExact}}, Enabled: true}
	if result := callIPC("physical_models.upsert", paramsFor(physical)); !result.OK {
		t.Fatalf("create physical model: %s", result.Error)
	}
	combo := store.ComboModel{Name: "isolation-role", Members: []store.ModelReference{{Kind: store.PhysicalReference, ID: physical.Name}}, Discoverable: true, Enabled: true}
	if result := callIPC("combo_models.upsert", paramsFor(combo)); !result.OK {
		t.Fatalf("create combo model: %s", result.Error)
	}

	baseline := readBundle()
	if len(baseline.Providers) != 1 || len(baseline.Connections) != 1 || len(baseline.Physical) != 1 || len(baseline.Combos) != 1 {
		t.Fatalf("fixture bundle lacks provider/connection/physical/combo: %#v", baseline)
	}
	dataPlane := httptest.NewServer(d.server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}))
	defer dataPlane.Close()
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, dataPlane.URL+"/v1/chat/completions", strings.NewReader(`{"model":"isolation-role","messages":[{"role":"user","content":"hold"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	streamResponse, err := dataPlane.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResponse.Body)
		t.Fatalf("in-flight request status=%d body=%s", streamResponse.StatusCode, body)
	}
	streamReader := bufio.NewReader(streamResponse.Body)
	var firstLineText string
	firstLine := make(chan error, 1)
	go func() {
		line, readErr := streamReader.ReadString('\n')
		firstLineText = line
		if readErr == nil && !strings.HasPrefix(line, "data:") {
			readErr = fmt.Errorf("unexpected stream line %q", line)
		}
		firstLine <- readErr
	}()
	select {
	case err := <-firstLine:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not emit its first event")
	}
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("fake upstream did not hold the in-flight request")
	}

	control, serving := statusVersions()
	transformRef := configIsolationTransform{}.Definition().Ref
	invalidCases := []struct {
		name   string
		mutate func(*controlplane.ConfigBundle)
	}{
		{name: "missing module build", mutate: func(bundle *controlplane.ConfigBundle) {
			bundle.TransformBindings = []kernel.TransformBinding{{ID: "module-pin", TransformRef: transformRef, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}, Options: json.RawMessage(`{"label":"valid"}`)}}
			bundle.Dependencies, _ = d.strategyCatalog.Extensions().LockDependencies(append(bundle.Dependencies.Roots, transformRef))
			bundle.Dependencies.Modules[0].Version = "missing-build"
		}},
		{name: "wrong host contract", mutate: func(bundle *controlplane.ConfigBundle) {
			bundle.TransformBindings = []kernel.TransformBinding{{ID: "module-pin", TransformRef: transformRef, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}, Options: json.RawMessage(`{"label":"valid"}`)}}
			bundle.Dependencies, _ = d.strategyCatalog.Extensions().LockDependencies(append(bundle.Dependencies.Roots, transformRef))
			bundle.Dependencies.Modules[0].HostContractVersion++
		}},
		{name: "invalid exact transform ref", mutate: func(bundle *controlplane.ConfigBundle) {
			bundle.TransformBindings = []kernel.TransformBinding{{ID: "invalid-ref", TransformRef: extensions.Ref{Kind: kernel.RequestTransformKind, ID: transformRef.ID, ContractVersion: transformRef.ContractVersion + 1}, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}
			bundle.Dependencies.Roots = append(bundle.Dependencies.Roots, extensions.Ref{Kind: kernel.RequestTransformKind, ID: transformRef.ID, ContractVersion: transformRef.ContractVersion + 1})
		}},
		{name: "schema-invalid transform options", mutate: func(bundle *controlplane.ConfigBundle) {
			bundle.TransformBindings = []kernel.TransformBinding{{ID: "invalid-options", TransformRef: transformRef, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}, Options: json.RawMessage(`{"label":7}`)}}
			bundle.Dependencies, _ = d.strategyCatalog.Extensions().LockDependencies(append(bundle.Dependencies.Roots, transformRef))
		}},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			candidate := baseline
			candidate.Dependencies.Modules = append([]extensions.ModuleRef(nil), baseline.Dependencies.Modules...)
			candidate.Dependencies.Roots = append([]extensions.Ref(nil), baseline.Dependencies.Roots...)
			candidate.TransformBindings = append([]kernel.TransformBinding(nil), baseline.TransformBindings...)
			test.mutate(&candidate)
			if test.name == "schema-invalid transform options" {
				if err := d.kernel.ValidateTransformBindings(candidate.TransformBindings); err == nil || !strings.Contains(strings.ToLower(err.Error()), "schema") {
					t.Fatalf("test transform options do not violate the registered schema: %v", err)
				}
			}
			response, applyErr := callIPCExpectedFailure("config.apply", paramsFor(candidate))
			if applyErr == nil || response.OK {
				t.Fatalf("invalid bundle was accepted or IPC did not report its rejection: response=%#v err=%v", response, applyErr)
			}
			persisted, err := controlplane.ExportBundle(d.store, d.strategyCatalog.Extensions())
			if err != nil {
				t.Fatalf("direct persisted export after %s: %v", test.name, err)
			}
			if !reflect.DeepEqual(baseline, persisted) {
				t.Fatalf("%s changed independently exported persisted config\nbefore=%#v\nafter=%#v", test.name, baseline, persisted)
			}
			assertUnchanged(baseline, control, serving, test.name)
		})
	}

	release()
	remaining, err := io.ReadAll(streamReader)
	remaining = append([]byte(firstLineText), remaining...)
	if err != nil || !strings.Contains(string(remaining), "prior-route") || !strings.Contains(string(remaining), "[DONE]") {
		t.Fatalf("held request did not complete on its pinned route: body=%q err=%v", remaining, err)
	}
	newRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"isolation-role","messages":[{"role":"user","content":"after rejects"}],"stream":true}`))
	newRequest.Header.Set("Content-Type", "application/json")
	newResponse := httptest.NewRecorder()
	d.server.HandlerWithOptions(api.HandlerOptions{DataPlane: true}).ServeHTTP(newResponse, newRequest)
	if newResponse.Code != http.StatusOK || !strings.Contains(newResponse.Body.String(), "prior-route") || !strings.Contains(newResponse.Body.String(), "[DONE]") {
		t.Fatalf("new inference did not use the prior route: status=%d body=%q", newResponse.Code, newResponse.Body.String())
	}

	accepted := baseline
	if applied := callIPC("config.apply", paramsFor(accepted)); !applied.OK {
		t.Fatalf("valid config apply after rejections failed: %s", applied.Error)
	}
	if got := readBundle(); !reflect.DeepEqual(got, accepted) {
		t.Fatalf("valid config apply did not publish requested config\ngot=%#v\nwant=%#v", got, accepted)
	}
	updatedControl, updatedServing := statusVersions()
	if updatedControl <= control || updatedServing != updatedControl {
		t.Fatalf("accepted apply did not publish a new coherent snapshot: before=%d/%d after=%d/%d", control, serving, updatedControl, updatedServing)
	}
}
