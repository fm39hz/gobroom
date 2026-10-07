package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type pipelineRequest struct{}

func (pipelineRequest) ID() string { return "fixture-request" }
func (pipelineRequest) DescribeCompatibility(input kernel.CompatibilityContext) []kernel.FacetMapping {
	return []kernel.FacetMapping{{Facet: kernel.FacetWireRequest, Paths: []string{"fixture.request"}, Disposition: kernel.FacetPreserved}}
}
func (pipelineRequest) Prepare(context.Context, kernel.NormalizedRequest, kernel.Route, kernel.Credential) (kernel.UpstreamRequest, error) {
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: "/generate"}, nil
}

type pipelineTransport struct{}

func (pipelineTransport) ID() string { return "fixture-transport" }
func (pipelineTransport) Execute(context.Context, kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	return kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("fixture"))}, nil
}

type scriptedResponseDecoder struct {
	name              string
	emitBeforeFailure bool
	attempts          *[]string
}

type uppercaseSemanticText struct{}

func (uppercaseSemanticText) Definition() kernel.ResponseTransformDefinition {
	return kernel.ResponseTransformDefinition{Ref: extensions.Ref{Kind: kernel.ResponseTransformKind, ID: "fixture.uppercase-response-text.v1", ContractVersion: 1}, ImplementationVersion: "1", Label: "Uppercase fixture text", Description: "Prove semantic response transforms run before rendering.", Effects: []kernel.ResponseTransformEffect{kernel.ResponseEffectText}}
}
func (uppercaseSemanticText) ApplyResponse(_ context.Context, event kernel.ResponseEvent, _ json.RawMessage) (kernel.ResponseEvent, error) {
	if event.Kind == kernel.EventTextDelta {
		event.Text = strings.Map(unicode.ToUpper, event.Text)
	}
	return event, nil
}

func (d scriptedResponseDecoder) ID() string { return d.name }
func (d scriptedResponseDecoder) PossibleEvents() []kernel.ResponseEventKind {
	return []kernel.ResponseEventKind{kernel.EventTextDelta, kernel.EventContentBlockEnd, kernel.EventResponseComplete}
}
func (d scriptedResponseDecoder) ClassifyError(int, []byte) kernel.ErrorClass {
	return kernel.ErrorRetryable
}
func (d scriptedResponseDecoder) Decode(_ context.Context, _ kernel.UpstreamResponse, emit func(kernel.ResponseEvent) error, _ kernel.StreamHooks) error {
	*d.attempts = append(*d.attempts, d.name)
	if d.emitBeforeFailure {
		if err := emit(kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "partial"}); err != nil {
			return err
		}
		return errors.New("decode failed after output delta")
	}
	if d.name == "broken" {
		return errors.New("decode failed before output")
	}
	if err := emit(kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "recovered"}); err != nil {
		return err
	}
	if err := emit(kernel.ResponseEvent{Kind: kernel.EventContentBlockEnd, StopReason: "stop"}); err != nil {
		return err
	}
	return emit(kernel.ResponseEvent{Kind: kernel.EventResponseComplete})
}

func TestComposedSemanticPipelineDoesNotReplayAcceptedUpstreamResponse(t *testing.T) {
	buildKernel := func(t *testing.T, failAfterWrite bool) (*kernel.Kernel, *[]string) {
		t.Helper()
		input := kernel.SnapshotInput{
			PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
			Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelCombo, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "broken-route"}, {Kind: kernel.MemberRoute, ID: "good-route"}}}},
			Routes: []kernel.Route{
				{ID: "broken-route", Protocol: kernel.Protocol("vendor.v1"), BaseURL: "https://provider.test/v1", Enabled: true, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"broken"}}}},
				{ID: "good-route", Protocol: kernel.Protocol("vendor.v1"), BaseURL: "https://provider.test/v1", Enabled: true, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {ContractVersion: 1, AdapterIDs: []string{"good"}}}},
			},
		}
		if !failAfterWrite {
			input.TransformBindings = []kernel.TransformBinding{{ID: "daemon.uppercase", TransformRef: extensions.Ref{Kind: kernel.ResponseTransformKind, ID: "fixture.uppercase-response-text.v1", ContractVersion: 1}, Enabled: true, Scope: kernel.TransformScope{Kind: kernel.TransformScopeDaemon}}}
		}
		snapshot, err := kernel.BuildSnapshot(input, 1)
		if err != nil {
			t.Fatal(err)
		}
		instance, err := kernel.New(snapshot, nil, 8)
		if err != nil {
			t.Fatal(err)
		}
		attempts := []string{}
		renderers := make(map[normalize.Format]kernel.ResponseRenderer)
		for _, renderer := range egress.Builtins() {
			renderers[renderer.ID()] = renderer
		}
		instance.Adapters["broken"] = kernel.ComposedAdapter{AdapterID: "broken", Endpoint: kernel.HTTPJSONEndpoint{}, Request: pipelineRequest{}, Transport: pipelineTransport{}, Response: scriptedResponseDecoder{name: "broken", emitBeforeFailure: failAfterWrite, attempts: &attempts}, ProviderFormat: normalize.Format("vendor.v1"), Renderers: renderers}
		instance.Adapters["good"] = kernel.ComposedAdapter{AdapterID: "good", Endpoint: kernel.HTTPJSONEndpoint{}, Request: pipelineRequest{}, Transport: pipelineTransport{}, Response: scriptedResponseDecoder{name: "good", attempts: &attempts}, ProviderFormat: normalize.Format("vendor.v1"), Renderers: renderers}
		return instance, &attempts
	}

	precommitKernel, precommitAttempts := buildKernel(t, false)
	if err := precommitKernel.ResponseTransforms.Register(uppercaseSemanticText{}); err != nil {
		t.Fatal(err)
	}
	bindings := precommitKernel.Snapshots.Load().TransformBindings
	if err := precommitKernel.ResponseTransforms.ValidateBindings(bindings); err != nil {
		t.Fatalf("validate response binding: %v", err)
	}
	probe, err := precommitKernel.ResponseTransforms.ApplyScopes(context.Background(), kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "probe"}, bindings, kernel.TransformScope{Kind: kernel.TransformScopeDaemon})
	if err != nil || probe.Text != "PROBE" {
		t.Fatalf("apply response binding probe=%#v err=%v", probe, err)
	}
	precommitWriter := httptest.NewRecorder()
	request := kernel.NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	err = precommitKernel.Execute(context.Background(), request, kernel.Credential{}, precommitWriter)
	var suppressed *kernel.ReplaySuppressedError
	if !errors.As(err, &suppressed) || suppressed.Effect != kernel.EffectAccepted {
		t.Fatalf("accepted response should suppress retry, got %v", err)
	}
	precommitKernel.Close()
	if len(*precommitAttempts) != 1 || (*precommitAttempts)[0] != "broken" || precommitWriter.Body.Len() != 0 {
		t.Fatalf("pre-commit attempts=%v body=%q", *precommitAttempts, precommitWriter.Body.String())
	}

	committedKernel, committedAttempts := buildKernel(t, true)
	committedWriter := httptest.NewRecorder()
	streamRequest := request
	streamRequest.Stream = true
	err = committedKernel.Execute(context.Background(), streamRequest, kernel.Credential{}, committedWriter)
	committedKernel.Close()
	if err == nil || len(*committedAttempts) != 1 || !strings.Contains(committedWriter.Body.String(), "partial") {
		t.Fatalf("post-commit err=%v attempts=%v body=%q", err, *committedAttempts, committedWriter.Body.String())
	}
}

func TestComposedSemanticPipelineBoundsEachDecodedResponseEvent(t *testing.T) {
	attempts := []string{}
	adapter := kernel.ComposedAdapter{
		AdapterID: "bounded-events", Endpoint: kernel.HTTPJSONEndpoint{}, Request: pipelineRequest{},
		Transport: pipelineTransport{}, Response: scriptedResponseDecoder{name: "bounded", attempts: &attempts},
		ProviderFormat: normalize.FormatOpenAIChat,
		Renderers:      map[normalize.Format]kernel.ResponseRenderer{normalize.FormatOpenAIChat: egress.OpenAIChat{}},
	}
	writer := httptest.NewRecorder()
	err := adapter.RenderResponse(context.Background(), kernel.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("fixture")),
	}, writer, normalize.FormatOpenAIChat, kernel.StreamHooks{MaxEventBytes: 4})
	if err == nil || !strings.Contains(err.Error(), "operation buffer limit") {
		t.Fatalf("oversized decoded event error=%v", err)
	}
	if writer.Body.Len() != 0 || len(attempts) != 1 {
		t.Fatalf("event limit should stop before renderer emission: body=%q attempts=%v", writer.Body.String(), attempts)
	}
}
