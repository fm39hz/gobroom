package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type issuerReplayAdapter struct {
	id         string
	failStatus int
	failures   int
	calls      int
	keys       []string
}

func (a *issuerReplayAdapter) ID() string { return a.id }
func (a *issuerReplayAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (*issuerReplayAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: route.ID}, nil
}
func (a *issuerReplayAdapter) Execute(_ context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	a.calls++
	a.keys = append(a.keys, request.Headers.Get("X-Upstream-Idempotency"))
	if a.calls <= a.failures {
		return UpstreamResponse{Status: a.failStatus, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader("ambiguous"))}, nil
	}
	return UpstreamResponse{Status: http.StatusOK, Headers: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (*issuerReplayAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (*issuerReplayAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if _, err := io.Copy(writer, response.Body); err != nil {
		return err
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func newReplayKeyKernel(t *testing.T, first, second *issuerReplayAdapter) *Kernel {
	t.Helper()
	catalog := extensions.NewCatalog()
	operationsRegistry, err := operations.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	operationRef := extensions.Ref{Kind: "operation", ID: "job.submit", ContractVersion: 1}
	if err := operationsRegistry.RegisterRawPayload(operationRef, "Submit job", "Idempotent job fixture.", json.RawMessage(`{"type":"object"}`), operations.ReplayWithKey); err != nil {
		t.Fatal(err)
	}
	contractCatalog, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	operationSnapshot, err := operationsRegistry.Seal(contractCatalog)
	if err != nil {
		t.Fatal(err)
	}
	operation := normalize.Operation(operationRef.ID)
	bindings := func(adapterID, idempotencyHeader string) map[normalize.Operation]RouteOperationBinding {
		result := RouteOperationBinding{ContractVersion: 1, AdapterIDs: []string{adapterID}}
		if idempotencyHeader != "" {
			result.IdempotencyHeaders = map[string]string{adapterID: idempotencyHeader}
		}
		return map[normalize.Operation]RouteOperationBinding{operation: result}
	}
	definitionA := extensions.Ref{Kind: "provider-definition", ID: "issuer-a", ContractVersion: 1}
	definitionB := extensions.Ref{Kind: "provider-definition", ID: "issuer-b", ContractVersion: 1}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "job", TargetRef: "job-model"}},
		Nodes: []ModelNode{{ID: "job-model", Kind: ModelPhysical, Strategy: StrategyFallback, Members: []MemberRef{
			{Kind: MemberRoute, ID: "route-a", Fidelity: FidelityExact}, {Kind: MemberRoute, ID: "route-b", Fidelity: FidelityExact},
		}}},
		Routes: []Route{
			{ID: "route-a", DefinitionID: "issuer-a", DefinitionRef: definitionA, ExternalModel: "job-v1", CredentialID: "conn-a", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: bindings(first.id, "X-Upstream-Idempotency")},
			{ID: "route-b", DefinitionID: "issuer-b", DefinitionRef: definitionB, ExternalModel: "job-v1", CredentialID: "conn-b", Enabled: true, Protocol: ProtocolOpenAIChat, OperationBindings: bindings(second.id, "")},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	k.Operations = operationSnapshot
	k.Adapters[first.id] = first
	k.Adapters[second.id] = second
	t.Cleanup(k.Close)
	return k
}

func replayKeyRequest(clientKey string) NormalizedRequest {
	return NormalizedRequest{
		Model: "job", Operation: normalize.Operation("job.submit"), OperationContractVersion: 1,
		OperationPayload: json.RawMessage(`{}`), SourceFormat: normalize.FormatOpenAIChat,
		Transport: normalize.TransportHints{IdempotencyKey: clientKey},
	}
}

func TestReplayWithKeyRetriesOnceWithinIssuerAndDerivesProviderScopedHeader(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusServiceUnavailable, failures: 1}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKeyKernel(t, first, second)
	if err := k.Execute(context.Background(), replayKeyRequest("client-key-123"), Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	if first.calls != 2 || second.calls != 0 || len(first.keys) != 2 {
		t.Fatalf("issuer A calls=%d keys=%v, issuer B calls=%d", first.calls, first.keys, second.calls)
	}
	if first.keys[0] == "" || first.keys[0] != first.keys[1] || first.keys[0] == "client-key-123" {
		t.Fatalf("provider key was missing, unstable, or unscoped: %v", first.keys)
	}
	route := Route{DefinitionRef: extensions.Ref{Kind: "provider-definition", ID: "issuer-a", ContractVersion: 1}, CredentialID: "conn-a", ExternalModel: "job-v1", Protocol: ProtocolOpenAIChat}
	if first.keys[0] != deriveIssuerIdempotencyKey("client-key-123", route, extensions.Ref{Kind: "operation", ID: "job.submit", ContractVersion: 1}) {
		t.Fatal("upstream key did not match the deterministic issuer-scoped derivation")
	}
	otherIssuer := route
	otherIssuer.DefinitionRef = extensions.Ref{Kind: "provider-definition", ID: "issuer-b", ContractVersion: 1}
	otherIssuer.CredentialID = "conn-b"
	if first.keys[0] == deriveIssuerIdempotencyKey("client-key-123", otherIssuer, extensions.Ref{Kind: "operation", ID: "job.submit", ContractVersion: 1}) {
		t.Fatal("same client key derived an identical upstream key for a different issuer")
	}
}

func TestReplayWithKeyNeverFallsBackAcrossIssuerAfterAmbiguousRetry(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusServiceUnavailable, failures: 2}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKeyKernel(t, first, second)
	err := k.Execute(context.Background(), replayKeyRequest("client-key-123"), Credential{}, httptest.NewRecorder())
	var suppressed *ReplaySuppressedError
	if !errors.As(err, &suppressed) || suppressed.Effect != EffectUnknown {
		t.Fatalf("ambiguous same-issuer retry result=%v", err)
	}
	if first.calls != 2 || second.calls != 0 {
		t.Fatalf("ambiguous dispatch crossed issuer boundary: A=%d B=%d", first.calls, second.calls)
	}
}

func TestReplayWithKeyWithoutClientProofDoesNotRetryAmbiguousDispatch(t *testing.T) {
	first := &issuerReplayAdapter{id: "issuer-a-adapter", failStatus: http.StatusServiceUnavailable, failures: 2}
	second := &issuerReplayAdapter{id: "issuer-b-adapter"}
	k := newReplayKeyKernel(t, first, second)
	err := k.Execute(context.Background(), replayKeyRequest(""), Credential{}, httptest.NewRecorder())
	var suppressed *ReplaySuppressedError
	if !errors.As(err, &suppressed) || first.calls != 1 || second.calls != 0 {
		t.Fatalf("missing client idempotency proof did not fail closed: err=%v A=%d B=%d", err, first.calls, second.calls)
	}
	if len(first.keys) != 1 || first.keys[0] != "" {
		t.Fatalf("upstream received an idempotency key without a client proof: %v", first.keys)
	}
}
