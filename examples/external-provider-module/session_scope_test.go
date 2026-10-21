package externalprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const sessionScopeDecoderID = "example.session-scope-responses"

func TestExternalModuleCanDeclareTypedContinuityRejection(t *testing.T) {
	outcome := provider.ClassifiedOutcome{
		Class: provider.ErrorTerminal, Cause: provider.CauseContinuityInvalid,
		Effect: provider.EffectRejected, Scope: provider.ScopeRequest,
		Retry: provider.RetryNever, Recovery: provider.CredentialRecoveryNone,
		ContinuityRecovery: provider.ContinuityRecoveryInvalidate,
		Evidence:           []provider.EvidenceSource{provider.EvidenceErrorBody},
	}
	if outcome.ContinuityRecovery != provider.ContinuityRecoveryInvalidate {
		t.Fatal("typed module outcome was not preserved")
	}
}

type sessionScopeModule struct{}

func (sessionScopeModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.session-scope", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (sessionScopeModule) Register(runtime *provider.RuntimeRegistry) error {
	// Reuse the registered codec through the public catalog, not an internal
	// package or a parallel implementation of the Responses wire protocol.
	builtins, err := provider.NewRuntimeRegistry()
	if err != nil {
		return err
	}
	snapshot, err := builtins.FreezeCatalog()
	if err != nil {
		return err
	}
	_, implementation, err := snapshot.Bind(provider.Ref{Kind: string(provider.PrimitiveResponseDecoder), ID: "openai-responses-sse", ContractVersion: 1}, nil)
	if err != nil {
		return err
	}
	decoder, ok := implementation.(provider.ResponseDecoder)
	if !ok {
		return fmt.Errorf("registered Responses primitive is not a decoder")
	}
	return runtime.RegisterResponseDecoder(sessionScopeResponsesDecoder{delegate: decoder})
}

type sessionScopeResponsesDecoder struct{ delegate provider.ResponseDecoder }

func (sessionScopeResponsesDecoder) ID() string { return sessionScopeDecoderID }
func (d sessionScopeResponsesDecoder) PossibleEvents() []provider.ResponseEventKind {
	return d.delegate.PossibleEvents()
}
func (d sessionScopeResponsesDecoder) ClassifyError(status int, body []byte) provider.ErrorClass {
	return d.delegate.ClassifyError(status, body)
}
func (d sessionScopeResponsesDecoder) Decode(ctx context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, hooks provider.StreamHooks) error {
	var responseID string
	err := d.delegate.Decode(ctx, response, func(event provider.ResponseEvent) error {
		if event.Kind == provider.EventResponseComplete {
			responseID = event.ResponseID
		}
		return emit(event)
	}, hooks)
	if err != nil {
		return err
	}
	if response.Headers.Get("X-Fixture-Session-Reset") == "true" {
		if hooks.OnSessionState != nil {
			hooks.OnSessionState(provider.SessionState{})
		}
		return nil
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.Headers.Get("X-Fixture-Session-Expires-At"))
	if err != nil {
		return err
	}
	if hooks.OnSessionState != nil {
		hooks.OnSessionState(provider.SessionState{ResponseID: responseID, ExpiresAt: expiresAt})
	}
	if response.Headers.Get("X-Fixture-Session-Fail") == "true" {
		return fmt.Errorf("fixture decode failed after private state")
	}
	return nil
}

// This public-daemon test observes continuity only at the fake upstream. The
// response decoder emits expiring opaque state through the public module hook;
// the real ingress, route selection, credential resolver and durable cache
// carry it between calls.
func TestExternalResponsesSessionScopeFollowsPrincipalAndEndpoint(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := runtime.Primitives.Definition(provider.ProviderDefinitionRef("openai-compatible-responses", 1))
	if !ok {
		t.Fatal("built-in Responses definition is unavailable")
	}
	definition.ID = "example-session-scope-responses"
	definition.DisplayName = "Responses session scope fixture"
	definition.DefaultCredentialType = "opaque_ticket"
	definition.AuthModes = []provider.AuthModeDefinition{{CredentialType: "opaque_ticket", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: opaqueStateAuthID, ContractVersion: 1}, Recovery: provider.CredentialRecoveryRefresh, Presentation: provider.CredentialPresentation{Header: "Authorization", Prefix: "Bearer "}}}
	binding := definition.Operations[provider.OperationResponses]
	binding.ResponseDecoder = provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: sessionScopeDecoderID, ContractVersion: 1}
	binding.ErrorClassifier = provider.PrimitiveRef{Kind: provider.PrimitiveErrorClassifier, ID: "http-json", ContractVersion: 1}
	binding.ErrorClassifierOptions = provider.ErrorClassifierOptions{HTTPJSON: &provider.HTTPJSONErrorClassifierOptions{
		AuthRecoverySignals:            []provider.HTTPJSONErrorSignal{{HTTPStatuses: []int{498}, Codes: []string{"credential_expired"}}},
		ContinuityResetSignals:         []provider.HTTPJSONErrorSignal{{HTTPStatuses: []int{400, 409}, Codes: []string{"previous_response_not_found"}}},
		ResponseContinuityResetSignals: []provider.HTTPJSONErrorSignal{{Codes: []string{"previous_response_not_found"}}},
	}}
	definition.Operations[provider.OperationResponses] = binding
	manifest, err := provider.EncodeManifest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "session-scope.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	type observedRequest struct {
		Token      string `json:"token"`
		Model      string `json:"model"`
		PreviousID string `json:"previous_response_id"`
	}
	requests := make(chan observedRequest, 32)
	var expireNext atomic.Bool
	var expireContinuation, resetContinuation, failContinuation atomic.Bool
	var missingParent atomic.Bool
	var acceptedFailure atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path=%q; want /v1/responses", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body observedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body.Token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		requests <- body
		if mode := acceptedFailure.Swap(0); mode != 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			if mode == 2 {
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_partial\",\"delta\":\"partial-text\"}\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"previous_response_not_found\",\"message\":\"private-stream-marker\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
			} else {
				_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"code\":\"previous_response_not_found\",\"message\":\"private-stream-marker\"}\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		if missingParent.Swap(false) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"previous_response_not_found","type":"invalid_request_error","message":"No response found for previous_response_id private-response-marker"}}`)
			return
		}
		if body.Token == "seed-runtime-ticket" && expireNext.CompareAndSwap(true, false) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(498)
			_, _ = w.Write([]byte(`{"error":{"code":"credential_expired"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		expiry := time.Now().Add(30 * time.Minute)
		if expireContinuation.Swap(false) {
			expiry = time.Now().Add(-time.Second)
		}
		w.Header().Set("X-Fixture-Session-Expires-At", expiry.UTC().Format(time.RFC3339Nano))
		if resetContinuation.Swap(false) {
			w.Header().Set("X-Fixture-Session-Reset", "true")
		}
		responseID := "respA"
		if failContinuation.Swap(false) {
			w.Header().Set("X-Fixture-Session-Fail", "true")
			responseID = "respFailed"
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":%q}}\n\ndata: [DONE]\n\n", responseID)
	}))
	defer upstream.Close()

	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0"}
	service := daemon.NewWithProviderModules(config, opaqueStateModule{}, sessionScopeModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Stop(context.Background()) }()
	rpc := func(method string, params map[string]any) json.RawMessage {
		t.Helper()
		response, callErr := daemon.CallIPC(context.Background(), config.IPCPath, daemon.IPCRequest{Method: method, Params: params})
		if callErr != nil || !response.OK {
			t.Fatalf("%s: ok=%v error=%v", method, response.OK, callErr)
		}
		encoded, marshalErr := json.Marshal(response.Result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return encoded
	}
	var node, connection struct {
		ID string `json:"ID"`
	}
	decodeID := func(raw json.RawMessage, target any) {
		t.Helper()
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	decodeID(rpc("providers.create", map[string]any{"name": "Session scope", "prefix": "scope", "baseUrl": upstream.URL + "/v1", "protocol": "openai_responses", "definitionID": definition.ID}), &node)
	seed := `{"ticket":"seed-runtime-ticket","subject":"seed-subject","workspace":"workspace-7","metadata":{"issued_by":"device-fixture"}}`
	decodeID(rpc("connections.create", map[string]any{"providerNodeID": node.ID, "name": "Session account", "credentialType": "opaque_ticket", "secret": seed}), &connection)
	_ = rpc("custom_models.upsert", map[string]any{"id": "scope-response-route", "providerNodeID": node.ID, "kind": "custom", "externalID": "gpt-test", "displayName": "gpt-test", "connectionIds": []string{connection.ID}})
	_ = rpc("physical_models.upsert", map[string]any{"name": "scope-response-physical", "identity": map[string]any{"canonicalName": "gpt-test"}, "sources": []map[string]any{{"routeId": "scope-response-route", "fidelity": "exact"}}, "enabled": true})
	_ = rpc("combo_models.upsert", map[string]any{"name": "scope-response-role", "members": []map[string]any{{"kind": "physical", "id": "scope-response-physical"}}, "discoverable": true, "enabled": true})
	var errorBody []byte
	var responseBody []byte
	var clientStreaming bool
	request := func() int {
		t.Helper()
		body, err := json.Marshal(struct {
			Model  string `json:"model"`
			Input  string `json:"input"`
			Stream bool   `json:"stream"`
		}{Model: "scope-response-role", Input: "continue", Stream: clientStreaming})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, "http://"+service.Addresses().HTTP+"/v1/responses", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-Id", "client-session")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		errorBody = nil
		responseBody, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode >= 400 {
			errorBody = responseBody
		}
		return resp.StatusCode
	}
	readRequest := func() observedRequest {
		t.Helper()
		select {
		case got := <-requests:
			return got
		case <-time.After(5 * time.Second):
			t.Fatal("request did not reach fake Responses endpoint")
			return observedRequest{}
		}
	}
	assertPrevious := func(want string) {
		t.Helper()
		if status := request(); status >= http.StatusBadRequest {
			t.Fatalf("Responses status=%d", status)
		}
		got := readRequest()
		if got.PreviousID != want {
			t.Fatalf("upstream previous_response_id=%q, want %q (token=%q)", got.PreviousID, want, got.Token)
		}
	}
	assertPrevious("")
	assertPrevious("respA")
	for _, mode := range []int32{1, 2} {
		clientStreaming = true
		acceptedFailure.Store(mode)
		status := request()
		if mode == 1 && status != http.StatusConflict || mode == 2 && status != http.StatusOK {
			t.Fatalf("accepted failure mode=%d status=%d body=%s", mode, status, responseBody)
		}
		if strings.Contains(string(responseBody), "private-stream-marker") || strings.Contains(string(responseBody), "[DONE]") || strings.Contains(string(responseBody), "response.completed") {
			t.Fatalf("error leaked or falsely completed: %s", responseBody)
		}
		if mode == 2 && (!strings.Contains(string(responseBody), "partial-text") || !strings.Contains(string(responseBody), "resend the complete conversation")) {
			t.Fatalf("committed stream lost text or error projection: %s", responseBody)
		}
		wantFailure := provider.ClientFailureForError(provider.ErrContinuityRestartRequired)
		if mode == 1 {
			var envelope struct {
				Error provider.ClientFailure `json:"error"`
			}
			if err := json.Unmarshal(responseBody, &envelope); err != nil || envelope.Error != wantFailure {
				t.Fatalf("HTTP failure lost typed identity: %s err=%v", responseBody, err)
			}
		} else {
			failures := 0
			for _, line := range strings.Split(string(responseBody), "\n") {
				data, ok := strings.CutPrefix(line, "data:")
				if !ok {
					continue
				}
				var envelope struct {
					Type     string `json:"type"`
					Response struct {
						Error provider.ClientFailure `json:"error"`
					} `json:"response"`
				}
				if err := json.Unmarshal([]byte(data), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Type == "response.failed" {
					failures++
					if envelope.Response.Error != wantFailure {
						t.Fatalf("stream failure lost typed identity: %#v", envelope.Response.Error)
					}
				}
			}
			if failures != 1 {
				t.Fatalf("terminal frames=%d want1", failures)
			}
		}
		if got := readRequest(); got.PreviousID != "respA" {
			t.Fatalf("accepted failure parent=%q", got.PreviousID)
		}
		select {
		case extra := <-requests:
			t.Fatalf("accepted error replayed: %#v", extra)
		default:
		}
		if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
			runDeviceRaceCLI(t, cli, config.IPCPath, "status")
		}
		if err := service.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		service = daemon.NewWithProviderModules(config, opaqueStateModule{}, sessionScopeModule{})
		if err := service.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		clientStreaming = false
		assertPrevious("")
		assertPrevious("respA")
	}
	var usages []struct {
		Status                    string
		InputTokens, OutputTokens int64
	}
	if err := json.Unmarshal(rpc("usage.list", map[string]any{"limit": 100}), &usages); err != nil {
		t.Fatal(err)
	}
	foundFailedUsage := false
	for _, usage := range usages {
		if usage.Status == "error" && usage.InputTokens == 3 && usage.OutputTokens == 2 {
			foundFailedUsage = true
		}
	}
	if !foundFailedUsage {
		t.Fatalf("partial failed provider usage was not retained: %#v", usages)
	}
	missingParent.Store(true)
	if status := request(); status != http.StatusConflict {
		t.Fatalf("continuation failure status=%d body=%s", status, errorBody)
	}
	var restart struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(errorBody, &restart); err != nil || restart.Error.Code != "continuity_restart_required" || strings.Contains(restart.Error.Message, "private-response-marker") {
		t.Fatalf("restart error was not safely normalized: %s err=%v", errorBody, err)
	}
	if got := readRequest(); got.PreviousID != "respA" {
		t.Fatalf("failed request parent=%q", got.PreviousID)
	}
	select {
	case extra := <-requests:
		t.Fatalf("rejected continuation was blindly replayed: %#v", extra)
	default:
	}
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "status")
	}
	// The 409 is sent only after the invalidation worker acknowledges its
	// ordered tombstone. Restart must not resurrect the rejected parent.
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = daemon.NewWithProviderModules(config, opaqueStateModule{}, sessionScopeModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPrevious("")
	assertPrevious("respA")
	failContinuation.Store(true)
	_ = request()
	readRequest()
	// The failed real call correctly cools the route down. Changing the model
	// discovery path resets provider health without changing the inference
	// endpoint or replacing its credential principal.
	_ = rpc("providers.update", map[string]any{"id": node.ID, "modelsPath": "/fixture-models"})
	assertPrevious("respA")
	expireContinuation.Store(true)
	assertPrevious("respA")
	assertPrevious("")
	resetContinuation.Store(true)
	assertPrevious("respA")
	assertPrevious("")

	// A 498 response exercises the registered generic auth recovery signal and
	// opaque credential refresher. The refreshed token changes while the
	// principal represented by this connection remains stable.
	expireNext.Store(true)
	if status := request(); status >= http.StatusBadRequest {
		t.Fatalf("credential recovery request status=%d", status)
	}
	firstAttempt := readRequest()
	if firstAttempt.Token != "seed-runtime-ticket" {
		t.Fatalf("refresh trigger used token %q", firstAttempt.Token)
	}
	// The retried operation is issued internally after 498. Its upstream body
	// is consumed here before the next explicit request.
	retry := readRequest()
	if retry.Token != "refreshed-runtime-ticket" {
		t.Fatalf("retry token=%q; want refreshed credential", retry.Token)
	}
	assertPrevious("respA")

	// Replacing the credential on the same connection is a new principal
	// generation. Existing client session state must not cross that boundary.
	replacement := `{"ticket":"replacement-ticket","subject":"replacement-subject","workspace":"workspace-7","metadata":{"issued_by":"device-fixture"}}`
	if cli := os.Getenv("GOBROOM_TEST_CLI"); cli != "" {
		runDeviceRaceCLI(t, cli, config.IPCPath, "connections", "update", "--id", connection.ID, "--secret", replacement)
	} else {
		_ = rpc("connections.update", map[string]any{"id": connection.ID, "secret": replacement})
	}
	if status := request(); status >= http.StatusBadRequest {
		t.Fatalf("request after credential replacement status=%d", status)
	}
	if got := readRequest(); got.PreviousID != "" {
		t.Fatalf("credential replacement leaked previous_response_id=%q", got.PreviousID)
	}

	// Change the provider endpoint using its public IPC update operation. The
	// same logical connection and client session now address a new realm.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body observedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode changed-realm request: %v", err)
		}
		body.Token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Fixture-Session-Expires-At", time.Now().Add(30*time.Minute).UTC().Format(time.RFC3339Nano))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"respA\"}}\n\ndata: [DONE]\n\n"))
	}))
	defer other.Close()
	_ = rpc("providers.update", map[string]any{"id": node.ID, "baseUrl": other.URL + "/v1"})
	if status := request(); status >= http.StatusBadRequest {
		t.Fatalf("request after endpoint update status=%d", status)
	}
	if got := readRequest(); got.PreviousID != "" {
		t.Fatalf("changed endpoint leaked previous_response_id=%q", got.PreviousID)
	}
	assertPrevious("respA")

	// A daemon restart reloads persisted state for the current principal and
	// endpoint. The same external session must still continue from that state.
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = daemon.NewWithProviderModules(config, opaqueStateModule{}, sessionScopeModule{})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPrevious("respA")
}
