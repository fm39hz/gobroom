package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type committedFailureAdapter struct{ attempts *atomic.Int32 }

type cancelAdapter struct{ started chan struct{} }
type fastAdapter struct{ attempts *atomic.Int32 }

type usageSourceAdapter struct{}

type usageSourceFixture struct{}

type snapshotPinAdapter struct {
	id      string
	entered chan<- struct{}
	release <-chan struct{}
}

func (a snapshotPinAdapter) ID() string { return a.id }
func (a snapshotPinAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (snapshotPinAdapter) Prepare(_ context.Context, _ NormalizedRequest, route Route, _ Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://" + route.ExternalModel}, nil
}
func (a snapshotPinAdapter) Execute(ctx context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	if a.entered != nil {
		a.entered <- struct{}{}
		select {
		case <-a.release:
		case <-ctx.Done():
			return UpstreamResponse{}, ctx.Err()
		}
	}
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.TrimPrefix(request.URL, "test://")))}, nil
}
func (snapshotPinAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (snapshotPinAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	_, err := io.Copy(writer, response.Body)
	if err == nil && hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return err
}

func (usageSourceFixture) EnrichUsage(_ context.Context, _ Route, headers http.Header, event UsageEvent) (UsageEvent, error) {
	value, err := strconv.ParseInt(headers.Get("X-Usage-Input-Tokens"), 10, 64)
	if err == nil {
		event.InputTokens = value
	}
	return event, err
}

func (a cancelAdapter) ID() string { return "cancel" }
func (a cancelAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a cancelAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://cancel"}, nil
}
func (a cancelAdapter) Execute(ctx context.Context, _ UpstreamRequest) (UpstreamResponse, error) {
	close(a.started)
	<-ctx.Done()
	return UpstreamResponse{}, ctx.Err()
}
func (a cancelAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a cancelAdapter) RenderResponse(context.Context, UpstreamResponse, http.ResponseWriter, normalize.Format, StreamHooks) error {
	return nil
}

func (a fastAdapter) ID() string { return "fast" }
func (a fastAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a fastAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://fast"}, nil
}
func (a fastAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	a.attempts.Add(1)
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}
func (a fastAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a fastAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	_, err := io.Copy(writer, response.Body)
	if err == nil && hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return err
}

func (a committedFailureAdapter) ID() string { return "committed-failure" }
func (a committedFailureAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (a committedFailureAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://"}, nil
}
func (a committedFailureAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	a.attempts.Add(1)
	return UpstreamResponse{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("partial"))}, nil
}
func (a committedFailureAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (a committedFailureAdapter) RenderResponse(_ context.Context, response UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	_, _ = io.Copy(writer, response.Body)
	err := errors.New("post-commit stream failure")
	if hooks.OnError != nil {
		hooks.OnError(err)
	}
	return err
}

func (usageSourceAdapter) ID() string { return "usage-source-fixture" }
func (usageSourceAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return fixtureCompatibilityPlan(input, normalize.FormatOpenAIChat, FidelityNative)
}
func (usageSourceAdapter) Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error) {
	return UpstreamRequest{Method: http.MethodPost, URL: "test://usage"}, nil
}
func (usageSourceAdapter) Execute(context.Context, UpstreamRequest) (UpstreamResponse, error) {
	return UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"X-Usage-Input-Tokens": []string{"23"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
}
func (usageSourceAdapter) ClassifyError(int, []byte) ErrorClass { return ErrorRetryable }
func (usageSourceAdapter) RenderResponse(_ context.Context, _ UpstreamResponse, _ http.ResponseWriter, _ normalize.Format, hooks StreamHooks) error {
	if hooks.OnComplete != nil {
		hooks.OnComplete(UsageEvent{Status: "ok"})
	}
	return nil
}

func TestKernelAppliesSelectedUsageSourceBeforeEmission(t *testing.T) {
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}},
		Nodes:        []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
		Routes:       []Route{{ID: "route", NodeID: "provider", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"usage-source-fixture"}, UsageSourceRef: extensions.Ref{Kind: "usage_source", ID: "usage-fixture", ContractVersion: 1}}}, Protocol: ProtocolOpenAIChat, Enabled: true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters["usage-source-fixture"] = usageSourceAdapter{}
	k.UsageSources[extensions.Ref{Kind: "usage_source", ID: "usage-fixture", ContractVersion: 1}] = usageSourceFixture{}
	if err := k.Execute(context.Background(), NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-k.Events:
		if event.InputTokens != 23 {
			t.Fatalf("usage source was not applied before emission: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("usage event not emitted")
	}
}

func TestSnapshotReloadDoesNotBlockStreamsAndPinsActiveRequests(t *testing.T) {
	buildSnapshot := func(version uint64, modelID, adapterID string) Snapshot {
		t.Helper()
		snapshot, err := BuildSnapshot(SnapshotInput{
			PublicModels: []PublicModel{{Name: "model", TargetRef: "physical"}},
			Nodes:        []ModelNode{{ID: "physical", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}},
			Routes: []Route{{
				ID: "route", Enabled: true, ExternalModel: modelID, Protocol: ProtocolOpenAIChat,
				OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{adapterID}}},
			}},
		}, version)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	oldSnapshot := buildSnapshot(1, "upstream-v1", "snapshot-pin-v1")
	k, err := New(oldSnapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	k.Adapters["snapshot-pin-v1"] = snapshotPinAdapter{id: "snapshot-pin-v1", entered: entered, release: release}
	k.Adapters["snapshot-pin-v2"] = snapshotPinAdapter{id: "snapshot-pin-v2"}
	type execution struct {
		writer *httptest.ResponseRecorder
		err    error
	}
	activeResults := make(chan execution, 2)
	request := NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	for range 2 {
		go func() {
			writer := httptest.NewRecorder()
			err := k.Execute(context.Background(), request, Credential{}, writer)
			activeResults <- execution{writer: writer, err: err}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("active requests did not reach the controlled upstream stream")
		}
	}
	if err := k.PublishSnapshot(buildSnapshot(2, "upstream-v2", "snapshot-pin-v2")); err != nil {
		t.Fatal(err)
	}
	newWriter := httptest.NewRecorder()
	if err := k.Execute(context.Background(), request, Credential{}, newWriter); err != nil {
		t.Fatalf("request after snapshot publication: %v", err)
	}
	if got := newWriter.Body.String(); got != "upstream-v2" {
		t.Fatalf("new request used stale snapshot route %q", got)
	}
	close(release)
	for range 2 {
		select {
		case result := <-activeResults:
			if result.err != nil {
				t.Fatalf("active request after reload: %v", result.err)
			}
			if got := result.writer.Body.String(); got != "upstream-v1" {
				t.Fatalf("active request changed snapshots mid-flight: got %q", got)
			}
		case <-time.After(time.Second):
			t.Fatal("active request did not finish after its original upstream was released")
		}
	}
}

func TestKernelDoesNotSwitchAfterResponseCommitment(t *testing.T) {
	attempts := &atomic.Int32{}
	snapshot, err := BuildSnapshot(SnapshotInput{
		PublicModels: []PublicModel{{Name: "role", TargetRef: "role"}},
		Nodes:        []ModelNode{{ID: "role", Kind: ModelCombo, Strategy: StrategyFallback, Members: []MemberRef{{Kind: MemberRoute, ID: "a"}, {Kind: MemberRoute, ID: "b"}}}},
		Routes:       []Route{{ID: "a", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"committed-failure"}}}, Protocol: ProtocolOpenAIChat, Enabled: true}, {ID: "b", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"committed-failure"}}}, Protocol: ProtocolOpenAIChat, Enabled: true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters["committed-failure"] = committedFailureAdapter{attempts: attempts}
	err = k.Execute(context.Background(), NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
	if err == nil || !strings.Contains(err.Error(), "post-commit") {
		t.Fatalf("err=%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("route switched after commitment: attempts=%d", attempts.Load())
	}
}

func TestKernelPropagatesCancellationDuringUpstreamExecution(t *testing.T) {
	started := make(chan struct{})
	snapshot, err := BuildSnapshot(SnapshotInput{PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}}, Nodes: []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}}, Routes: []Route{{ID: "route", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"cancel"}}}, Protocol: ProtocolOpenAIChat, Enabled: true}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters["cancel"] = cancelAdapter{started: started}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- k.Execute(ctx, NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder())
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("kernel did not propagate cancellation")
	}
}

func TestKernelConcurrentRequestsDoNotSerializeProviderExecution(t *testing.T) {
	attempts := &atomic.Int32{}
	snapshot, err := BuildSnapshot(SnapshotInput{PublicModels: []PublicModel{{Name: "model", TargetRef: "model"}}, Nodes: []ModelNode{{ID: "model", Kind: ModelPhysical, Members: []MemberRef{{Kind: MemberRoute, ID: "route", Fidelity: FidelityExact}}}}, Routes: []Route{{ID: "route", OperationBindings: map[normalize.Operation]RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"fast"}}}, Protocol: ProtocolOpenAIChat, Enabled: true}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(snapshot, nil, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	k.Adapters["fast"] = fastAdapter{attempts: attempts}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := k.Execute(context.Background(), NormalizedRequest{Model: "model", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, Credential{}, httptest.NewRecorder()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if attempts.Load() != 100 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
}
