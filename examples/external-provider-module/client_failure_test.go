package externalprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

// This is an independent client contract, deliberately not the encoder's Go
// envelope type or schema builder. Validate actual HTTP bytes before decoding.
const failureClientAcceptanceSchema = `{
  "type":"object","additionalProperties":false,
  "required":["declined","receipt"],
  "properties":{
    "declined":{"type":"object","additionalProperties":false,
      "required":["tag","category","description"],"properties":{
        "tag":{"type":"string","minLength":1},
        "category":{"type":"string","minLength":1},
        "description":{"type":"string","minLength":1,"maxLength":4096}}},
    "receipt":{"type":"object","additionalProperties":false,
      "required":["caller","task","http","effect","retry"],"properties":{
        "caller":{"type":"string","minLength":1,"maxLength":128},
        "task":{"type":"object","additionalProperties":false,
          "required":["kind","id","contractVersion"],"properties":{
            "kind":{"const":"operation"},"id":{"const":"example.jobs.create"},
            "contractVersion":{"const":2}}},
        "http":{"type":"integer","minimum":400,"maximum":599},
        "effect":{"enum":["not_dispatched","confirmed_rejection","accepted","unknown"]},
        "retry":{"const":"retry_never"},
        "model":{"type":"string","minLength":1,"maxLength":128}}},
    "hints":{"type":"object","additionalProperties":false,"properties":{
      "paths":{"type":"array","maxItems":32,"items":{"type":"string"}},
      "reasons":{"type":"array","maxItems":32,"items":{"type":"string"}}}}
  }
}`

type failureClientWire struct {
	Declined struct {
		Tag         string `json:"tag"`
		Category    string `json:"category"`
		Description string `json:"description"`
	} `json:"declined"`
	Receipt struct {
		Caller string                  `json:"caller"`
		Task   provider.Ref            `json:"task"`
		HTTP   int                     `json:"http"`
		Effect provider.UpstreamEffect `json:"effect"`
		Retry  provider.RetryAction    `json:"retry"`
		Model  string                  `json:"model"`
	} `json:"receipt"`
	Hints struct {
		Paths   []string `json:"paths"`
		Reasons []string `json:"reasons"`
	} `json:"hints"`
}

// The only provider-definition customization lives in this test. Production
// registration must delegate to the existing job decoder and its declarations.
func failureClientManifest(t *testing.T) []byte {
	t.Helper()
	definition := jobManifest()
	operation := provider.Operation("example.jobs.create")
	binding := definition.Operations[operation]
	binding.ResponseDecoder = provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "example.jobs.failure-client.create", ContractVersion: 2}
	definition.Operations[operation] = binding
	encoded, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func failureClientWireValidator(t *testing.T) func([]byte) {
	t.Helper()
	runtime, err := provider.NewRuntimeRegistryWithModules(JobModule{}, newFailureClientModule{})
	if err != nil {
		t.Fatal(err)
	}
	independent := provider.Ref{Kind: provider.SchemaKind, ID: "fixture.failure-client.expected-wire", ContractVersion: 1}
	if err := runtime.RegisterSchemaDocument(independent, json.RawMessage(failureClientAcceptanceSchema)); err != nil {
		t.Fatal(err)
	}
	if err := runtime.LoadDefinitionJSON(failureClientManifest(t)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return func(body []byte) {
		t.Helper()
		for _, ref := range []provider.Ref{independent, {Kind: provider.SchemaKind, ID: "example.jobs.failure-client.error", ContractVersion: 1}} {
			if err := snapshot.ValidateSchema(ref, json.RawMessage(body)); err != nil {
				t.Fatalf("wire failed schema %s: %v; body=%s", ref.Key(), err, body)
			}
		}
	}
}

type failureClientFixture struct {
	url      string
	ipc      string
	client   *http.Client
	calls    atomic.Int32
	mode     atomic.Int32
	accepted int
}

func newFailureClientFixture(t *testing.T, accepted int) *failureClientFixture {
	t.Helper()
	f := &failureClientFixture{client: &http.Client{Timeout: 5 * time.Second}, accepted: accepted}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" || r.Header.Get("Authorization") != "Bearer private-job-credential-4b7e" {
			t.Errorf("wrong upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var input struct{ Model, Prompt string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "job-generator" || input.Prompt != "render a fixture" {
			t.Errorf("real job codec input=%#v err=%v", input, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch f.mode.Load() {
		case 1:
			// The registered wrapper must fail before delegating/emitting any
			// event, preserving accepted upstream effect without client success.
			w.Header().Set("X-Example-Private-Decode-Failure", "true")
			w.WriteHeader(f.accepted)
			_, _ = io.WriteString(w, `{"id":"private-job-body-4b7e","status":"pending"}`)
		case 2:
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"private-quota-body-4b7e"}}`)
		default:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"client-job-42","status":"pending"}`)
		}
	}))
	t.Cleanup(upstream.Close)
	root := t.TempDir()
	manifests := filepath.Join(root, "providers")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifests, "jobs.json"), failureClientManifest(t), 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifests, ArtifactStoreDir: filepath.Join(root, "artifacts"), HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPToken: "client-key-4b7e"}
	f.ipc = config.IPCPath
	service := daemon.NewWithProviderModules(config, JobModule{}, newFailureClientModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	f.url = "http://" + service.Addresses().HTTP + "/v1/failure-client/jobs/create"
	rpc := func(method string, params map[string]any) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := daemon.CallIPC(ctx, config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if err != nil || !response.OK {
			t.Fatalf("%s: response=%#v err=%v", method, response, err)
		}
		encoded, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatal(err)
		}
		var result struct{ ID string }
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result.ID
	}
	node := rpc("providers.create", map[string]any{"name": "Failure client host", "prefix": "failurejobs", "baseUrl": upstream.URL + "/v1", "protocol": "example_jobs", "definitionID": "example-jobs"})
	connection := rpc("connections.create", map[string]any{"providerNodeID": node, "name": "Only account", "credentialType": "api_key", "secret": "private-job-credential-4b7e"})
	rpc("custom_models.upsert", map[string]any{"id": "job-route", "providerNodeID": node, "kind": "custom", "externalID": "job-generator", "displayName": "Job generator", "connectionIds": []string{connection}})
	rpc("physical_models.upsert", map[string]any{"name": "job-physical", "identity": map[string]any{"canonicalName": "job-generator"}, "sources": []map[string]any{{"routeId": "job-route", "fidelity": "exact"}}, "enabled": true})
	rpc("combo_models.upsert", map[string]any{"name": "failure-role", "members": []map[string]any{{"kind": "physical", "id": "job-physical"}}, "discoverable": true, "enabled": true})
	return f
}

func (f *failureClientFixture) call(t *testing.T, rid, body, token string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", rid)
	request.Header.Set("Idempotency-Key", "key-"+rid)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header.Clone(), encoded
}

func assertFailureClientWire(t *testing.T, validate func([]byte), status int, headers http.Header, body []byte, rid, model, tag, category, description string, wantStatus int, effect provider.UpstreamEffect) {
	t.Helper()
	validate(body)
	var wire failureClientWire
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	operation := provider.Ref{Kind: "operation", ID: "example.jobs.create", ContractVersion: 2}
	if status != wantStatus || wire.Receipt.HTTP != wantStatus || wire.Receipt.Effect != effect || wire.Receipt.Retry != provider.RetryNever || wire.Receipt.Task != operation || wire.Receipt.Caller != rid || headers.Get("X-Request-ID") != rid || wire.Receipt.Model != model {
		t.Fatalf("wrong host facts: HTTP=%d headers=%v wire=%s", status, headers, body)
	}
	if headers.Get("Content-Type") != "application/vnd.example.job-declined+json" || wire.Declined.Tag != tag || wire.Declined.Category != category || wire.Declined.Description != description {
		t.Fatalf("wrong client dialect: HTTP=%d headers=%v body=%s", status, headers, body)
	}
	if len(wire.Hints.Paths) != 0 || len(wire.Hints.Reasons) != 0 {
		t.Fatalf("fabricated diagnostics: %s", body)
	}
	// Include headers: upstream private headers must not become client headers.
	encodedHeaders, err := json.Marshal(headers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)+string(encodedHeaders)), "private-") || headers.Get("X-Example-Private-Decode-Failure") != "" {
		t.Fatalf("private data escaped: headers=%v body=%s", headers, body)
	}
}

func TestIndependentClientFailureBeforeDispatch(t *testing.T) {
	validate := failureClientWireValidator(t)
	for _, tc := range []struct {
		name, body, token, tag, category, description, model string
		status                                               int
	}{
		{"auth", `{"private-auth-body":true}`, "private-wrong-client-key", "authentication_required", "authentication_error", "data plane authentication required", "", 401},
		{"decode", `{"model":"failure-role","private-ingress-body":"secret",`, "client-key-4b7e", "invalid_request", "invalid_request_error", "request could not be decoded", "", 400},
		{"input-schema", `{"model":"failure-role","operationPayload":{"prompt":"render a fixture","private-schema-property":"secret"}}`, "client-key-4b7e", "invalid_request", "invalid_request_error", "invalid request", "failure-role", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFailureClientFixture(t, http.StatusAccepted)
			rid := "caller-" + tc.name
			status, headers, body := f.call(t, rid, tc.body, tc.token)
			assertFailureClientWire(t, validate, status, headers, body, rid, tc.model, tc.tag, tc.category, tc.description, tc.status, provider.EffectNotDispatched)
			if got := f.calls.Load(); got != 0 {
				t.Fatalf("rejected client input dispatched %d upstream calls", got)
			}
		})
	}
}

// A separately supplied safe context checks the encoder mapping, independently
// from the daemon tests that check which facts the host supplies at each stage.
func TestIndependentClientFailureEncoderPreservesHostFactsAndDiagnostics(t *testing.T) {
	validate := failureClientWireValidator(t)
	input := provider.ClientFailureContext{
		Failure:      provider.ClientFailure{Code: "public_job_declined", Type: "input_error", Message: "choose a supported job size"},
		Diagnostics:  provider.ClientFailureDiagnostics{Paths: []string{"/operationPayload/size"}, Reasons: []string{"unsupported_size"}},
		OperationRef: provider.Ref{Kind: "operation", ID: "example.jobs.create", ContractVersion: 2},
		RequestID:    "caller-safe-context", Model: "failure-role", Status: 422,
		Effect: provider.EffectRejected, Retry: provider.RetryNever,
	}
	// This boundary accepts only the public safe context. The encoder has no
	// ResponseWriter, request, credentials, raw upstream body, or original error.
	var encoder provider.FailureEncoder = failureClientEncoder{}
	response, err := encoder.RenderFailure(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	validate([]byte(response.Body))
	var wire failureClientWire
	if err := json.Unmarshal([]byte(response.Body), &wire); err != nil {
		t.Fatal(err)
	}
	if !response.Valid() || response.ContentType != "application/vnd.example.job-declined+json" || wire.Declined.Tag != "public_job_declined" || wire.Declined.Category != "input_error" || wire.Declined.Description != "choose a supported job size" || wire.Receipt.Caller != "caller-safe-context" || wire.Receipt.Task != input.OperationRef || wire.Receipt.Model != "failure-role" || wire.Receipt.HTTP != 422 || wire.Receipt.Effect != provider.EffectRejected || wire.Receipt.Retry != provider.RetryNever {
		t.Fatalf("encoder changed safe host facts: %#v", response)
	}
	if len(wire.Hints.Paths) != 1 || wire.Hints.Paths[0] != "/operationPayload/size" || len(wire.Hints.Reasons) != 1 || wire.Hints.Reasons[0] != "unsupported_size" {
		t.Fatalf("explicit diagnostics were not mapped: %s", response.Body)
	}
}

func TestIndependentClientFailureAcceptedBeforeFirstByteDoesNotReplay(t *testing.T) {
	validate := failureClientWireValidator(t)
	for _, accepted := range []int{http.StatusOK, http.StatusAccepted} {
		t.Run(http.StatusText(accepted), func(t *testing.T) {
			f := newFailureClientFixture(t, accepted)
			request := `{"model":"failure-role","operationPayload":{"prompt":"render a fixture"}}`
			// Prove this registration uses the existing job artifact/renderer path.
			status, _, body := f.call(t, "caller-success", request, "client-key-4b7e")
			var success jobEnvelope
			if err := json.Unmarshal(body, &success); err != nil || status != 200 || success.Result.ID != "client-job-42" || success.Result.Status != "pending" || len(success.Artifacts) != 1 || success.Artifacts[0].TypeRef != jobArtifact {
				t.Fatalf("normal job result was not preserved: HTTP=%d body=%s err=%v", status, body, err)
			}
			before := f.calls.Load()
			if before != 1 {
				t.Fatalf("normal request dispatched %d times", before)
			}
			f.mode.Store(1)
			status, headers, body := f.call(t, "caller-accepted", request, "client-key-4b7e")
			assertFailureClientWire(t, validate, status, headers, body, "caller-accepted", "failure-role", "upstream_error", "upstream_error", "upstream request failed", 502, provider.EffectAccepted)
			if got := f.calls.Load() - before; got != 1 {
				t.Fatalf("accepted decode failure dispatched %d times; automatic replay forbidden", got)
			}
		})
	}
}

func TestIndependentClientFailureNoEligibleRouteAfterRealCooldown(t *testing.T) {
	validate := failureClientWireValidator(t)
	f := newFailureClientFixture(t, http.StatusAccepted)
	f.mode.Store(2)
	request := `{"model":"failure-role","operationPayload":{"prompt":"render a fixture"}}`
	status, headers, body := f.call(t, "caller-quota", request, "client-key-4b7e")
	// A dispatched rejection reports the authoritative effect. It is not the
	// same as a later request finding no eligible route at all.
	assertFailureClientWire(t, validate, status, headers, body, "caller-quota", "failure-role", "upstream_error", "upstream_error", "upstream request failed", 502, provider.EffectRejected)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("429 cooldown setup dispatched %d times", got)
	}
	// No private scheduler mutation: the real HTTP Retry-After made this sole
	// route ineligible. ErrNoRoute alone must still conservatively mean unknown.
	status, headers, body = f.call(t, "caller-cooling", request, "client-key-4b7e")
	assertFailureClientWire(t, validate, status, headers, body, "caller-cooling", "failure-role", "no_route", "upstream_error", "no usable route", 503, provider.EffectUnknown)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("ineligible route dispatched again: total calls=%d", got)
	}
}

func TestInstalledCLIClientFailureCatalogAndControl(t *testing.T) {
	installedCLI := os.Getenv("GOBROOM_TEST_CLI")
	if installedCLI == "" {
		t.Skip("installed client-failure acceptance requires GOBROOM_TEST_CLI; dedicated target enforces it")
	}
	f := newFailureClientFixture(t, http.StatusAccepted)
	cli := func(input io.Reader, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, installedCLI, append([]string{"--ipc", f.ipc, "--json"}, args...)...)
		command.Stdin = input
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("installed CLI %v: %v: %s", args, err, output)
		}
		return output
	}
	checkStatus := func(command string) {
		t.Helper()
		var status struct {
			Name           string `json:"name"`
			Status         string `json:"status"`
			ControlVersion uint64 `json:"controlSnapshotVersion"`
			ServingVersion uint64 `json:"servingSnapshotVersion"`
		}
		if err := json.Unmarshal(cli(nil, command), &status); err != nil {
			t.Fatal(err)
		}
		if status.Name != "gobroomd" || status.Status != "ok" || status.ControlVersion == 0 || status.ControlVersion != status.ServingVersion {
			t.Fatalf("installed %s did not report a healthy serving snapshot: %#v", command, status)
		}
	}
	operation := provider.Ref{Kind: "operation", ID: "example.jobs.create", ContractVersion: 2}
	decoder := provider.Ref{Kind: string(provider.PrimitiveResponseDecoder), ID: "example.jobs.failure-client.create", ContractVersion: 2}
	inputSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.create.input", ContractVersion: 1}
	resultSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.result", ContractVersion: 2}
	errorSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.failure-client.error", ContractVersion: 1}
	checkCatalog := func() {
		t.Helper()
		var catalog struct {
			Fingerprint string                         `json:"fingerprint"`
			Descriptors []provider.ExtensionDescriptor `json:"descriptors"`
			Schemas     []struct {
				Ref      provider.Ref    `json:"ref"`
				Digest   string          `json:"digest"`
				Document json.RawMessage `json:"document"`
			} `json:"schemas"`
		}
		if err := json.Unmarshal(cli(nil, "extensions", "catalog"), &catalog); err != nil {
			t.Fatal(err)
		}
		if catalog.Fingerprint == "" {
			t.Fatal("installed catalog omitted its fingerprint")
		}
		descriptors := make(map[provider.Ref]provider.ExtensionDescriptor, len(catalog.Descriptors))
		for _, descriptor := range catalog.Descriptors {
			descriptors[descriptor.Ref] = descriptor
		}
		ingressRef := provider.Ref{Kind: "client-ingress", ID: "example.jobs.failure-client.json", ContractVersion: 1}
		ingress, ok := descriptors[ingressRef]
		module := provider.ModuleRef{ID: "example.jobs.failure-client", Version: "1.0.0", HostContractVersion: 14}
		if !ok || ingress.Module == nil || *ingress.Module != module {
			t.Fatalf("installed catalog omitted the ingress or host-14 module binding: %#v", ingress)
		}
		var ingressLifecycle, failureLifecycle, operationDependency bool
		for _, capability := range ingress.LifecycleCapabilities {
			ingressLifecycle = ingressLifecycle || capability == "client.ingress"
			failureLifecycle = failureLifecycle || capability == "client.failure"
		}
		for _, dependency := range ingress.Dependencies {
			operationDependency = operationDependency || dependency == operation
		}
		if !ingressLifecycle || !failureLifecycle || !operationDependency {
			t.Fatalf("installed ingress lost its failure lifecycle or exact operation binding: %#v", ingress)
		}
		decoded, ok := descriptors[decoder]
		if !ok || decoded.Module == nil || *decoded.Module != module {
			t.Fatalf("installed catalog omitted the exact wrapper decoder binding: %#v", decoded)
		}
		job, ok := descriptors[operation]
		if !ok || job.InputSchemaRef == nil || *job.InputSchemaRef != inputSchema || job.ResultSchemaRef == nil || *job.ResultSchemaRef != resultSchema {
			t.Fatalf("installed catalog lost the job's typed schema refs: %#v", job)
		}
		for _, want := range []provider.Ref{inputSchema, resultSchema, errorSchema} {
			found := false
			for _, schema := range catalog.Schemas {
				if schema.Ref != want {
					continue
				}
				found = true
				uri, err := want.URI()
				if err != nil {
					t.Fatal(err)
				}
				var document struct {
					ID      string `json:"$id"`
					Dialect string `json:"$schema"`
				}
				if err := json.Unmarshal(schema.Document, &document); err != nil {
					t.Fatal(err)
				}
				if schema.Digest == "" || document.ID != uri || document.Dialect != "https://json-schema.org/draft/2020-12/schema" {
					t.Fatalf("installed schema lost its typed ref binding: ref=%#v document=%s", schema.Ref, schema.Document)
				}
			}
			if !found {
				t.Fatalf("installed catalog omitted schema %#v", want)
			}
		}
	}
	checkStatus("status")
	checkCatalog()
	checkStatus("reload")
	checkCatalog()
	// Export and validate only this temporary daemon's configuration. Preserve
	// the exported bytes as stdin; no live config, apply, or hand-built lock.
	exported := cli(nil, "config-export")
	var bundle struct {
		Version             int                           `json:"version"`
		ProviderDefinitions []provider.ProviderDefinition `json:"providerDefinitions"`
	}
	if err := json.Unmarshal(exported, &bundle); err != nil {
		t.Fatal(err)
	}
	var bound bool
	for _, definition := range bundle.ProviderDefinitions {
		if definition.ID != "example-jobs" || definition.ContractVersion != 1 {
			continue
		}
		binding, ok := definition.Operations[provider.Operation("example.jobs.create")]
		wantDecoder := provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "example.jobs.failure-client.create", ContractVersion: 2}
		wantCodec := provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "example.jobs.create", ContractVersion: 1}
		bound = ok && binding.TaskRef != nil && *binding.TaskRef == operation && binding.ResponseDecoder == wantDecoder && binding.RequestCodec == wantCodec
	}
	if bundle.Version != 9 || !bound {
		t.Fatalf("installed config export lost the exact job operation/codec/decoder binding: %#v", bundle)
	}
	var validated struct {
		Valid   bool `json:"valid"`
		Version int  `json:"version"`
	}
	if err := json.Unmarshal(cli(strings.NewReader(string(exported)), "config-validate"), &validated); err != nil {
		t.Fatal(err)
	}
	if !validated.Valid || validated.Version != bundle.Version {
		t.Fatalf("installed config validation rejected the exported fixture: %#v", validated)
	}
	checkStatus("status")
	if got := f.calls.Load(); got != 0 {
		t.Fatalf("catalog/control acceptance dispatched %d inference requests", got)
	}
}
