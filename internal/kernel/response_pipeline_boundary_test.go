package kernel_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	egress "github.com/fm39hz/gobroom/internal/adapter/renderers"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type pipelineRequest struct{}

func (pipelineRequest) ID() string { return "fixture-request" }
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
	return kernel.ResponseTransformDefinition{ID: "fixture.uppercase-response-text.v1", Label: "Uppercase fixture text", Description: "Prove semantic response transforms run before rendering.", Effects: []kernel.ResponseTransformEffect{kernel.ResponseEffectText}}
}
func (uppercaseSemanticText) ApplyResponse(_ context.Context, event kernel.ResponseEvent) (kernel.ResponseEvent, error) {
	if event.Kind == kernel.EventTextDelta {
		event.Text = strings.Map(unicode.ToUpper, event.Text)
	}
	return event, nil
}

func (d scriptedResponseDecoder) ID() string { return d.name }
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

func TestComposedSemanticResponsePipelineRetriesOnlyBeforeRendererCommit(t *testing.T) {
	buildKernel := func(t *testing.T, failAfterWrite bool) (*kernel.Kernel, *[]string) {
		t.Helper()
		snapshot, err := kernel.BuildSnapshot(kernel.SnapshotInput{
			PublicModels: []kernel.PublicModel{{Name: "role", TargetRef: "role"}},
			Nodes:        []kernel.ModelNode{{ID: "role", Kind: kernel.ModelCombo, Strategy: kernel.StrategyFallback, Members: []kernel.MemberRef{{Kind: kernel.MemberRoute, ID: "broken-route"}, {Kind: kernel.MemberRoute, ID: "good-route"}}}},
			Routes: []kernel.Route{
				{ID: "broken-route", Protocol: kernel.Protocol("vendor.v1"), BaseURL: "https://provider.test/v1", Enabled: true, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{"broken"}}}},
				{ID: "good-route", Protocol: kernel.Protocol("vendor.v1"), BaseURL: "https://provider.test/v1", Enabled: true, OperationBindings: map[normalize.Operation]kernel.RouteOperationBinding{normalize.OperationChatGenerate: {AdapterIDs: []string{"good"}}}},
			},
		}, 1)
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
	precommitWriter := httptest.NewRecorder()
	request := kernel.NormalizedRequest{Model: "role", Operation: normalize.OperationChatGenerate, SourceFormat: normalize.FormatOpenAIChat}
	if err := precommitKernel.Execute(context.Background(), request, kernel.Credential{}, precommitWriter); err != nil {
		t.Fatal(err)
	}
	precommitKernel.Close()
	if len(*precommitAttempts) != 2 || (*precommitAttempts)[0] != "broken" || (*precommitAttempts)[1] != "good" || !strings.Contains(precommitWriter.Body.String(), "RECOVERED") {
		t.Fatalf("pre-commit attempts=%v body=%q", *precommitAttempts, precommitWriter.Body.String())
	}

	committedKernel, committedAttempts := buildKernel(t, true)
	committedWriter := httptest.NewRecorder()
	streamRequest := request
	streamRequest.Stream = true
	err := committedKernel.Execute(context.Background(), streamRequest, kernel.Credential{}, committedWriter)
	committedKernel.Close()
	if err == nil || len(*committedAttempts) != 1 || !strings.Contains(committedWriter.Body.String(), "partial") {
		t.Fatalf("post-commit err=%v attempts=%v body=%q", err, *committedAttempts, committedWriter.Body.String())
	}
}
