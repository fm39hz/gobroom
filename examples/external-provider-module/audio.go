package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/fm39hz/gobroom/pkg/provider"
)

// AudioModule is intentionally a separate Go module. It contributes only the
// audio wire codec and semantic task; GoBroom owns shared HTTP transport,
// credentials, manifests, model graph and daemon lifecycle.
type AudioModule struct{}

func (AudioModule) ID() string { return "example.audio" }

func (AudioModule) Register(runtime *provider.RuntimeRegistry) error {
	task := provider.Ref{Kind: "operation", ID: "audio.transcribe", ContractVersion: 1}
	if err := runtime.Primitives.Operations.RegisterRawPayload(task, "Audio transcription", "Transcribe an audio resource.", json.RawMessage(`{"type":"object","properties":{"audio_url":{"type":"string","minLength":1},"language":{"type":"string"}},"required":["audio_url"],"additionalProperties":false}`), provider.ReplayNever); err != nil {
		return err
	}
	if err := runtime.RegisterRequestCodec(audioRequestCodec{}); err != nil {
		return err
	}
	return runtime.RegisterResponseDecoder(audioResponseDecoder{})
}

type audioRequestCodec struct{}

func (audioRequestCodec) ID() string { return "example-audio-json" }
func (audioRequestCodec) DescribeCompatibility(input provider.CompatibilityContext) []provider.FacetMapping {
	return []provider.FacetMapping{{Facet: provider.FacetWireRequest, Paths: []string{"operationPayload"}, Disposition: provider.FacetTranslated}}
}
func (audioRequestCodec) Prepare(_ context.Context, request provider.NormalizedRequest, _ provider.Route, credential provider.Credential) (provider.UpstreamRequest, error) {
	if len(request.OperationPayload) == 0 || !json.Valid(request.OperationPayload) {
		return provider.UpstreamRequest{}, fmt.Errorf("audio payload must be valid JSON")
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+credential.Secret)
	headers.Set("Content-Type", "application/json")
	return provider.UpstreamRequest{Method: http.MethodPost, URL: "/audio/transcriptions", Headers: headers, Body: bytes.NewReader(request.OperationPayload)}, nil
}

type audioResponseDecoder struct{}

func (audioResponseDecoder) ID() string { return "example-audio-json" }
func (audioResponseDecoder) PossibleEvents() []provider.ResponseEventKind {
	return []provider.ResponseEventKind{provider.EventTextDelta, provider.EventResponseComplete}
}
func (audioResponseDecoder) ClassifyError(status int, _ []byte) provider.ErrorClass {
	if status >= http.StatusBadRequest {
		return "terminal"
	}
	return "retryable"
}
func (audioResponseDecoder) Decode(_ context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, _ provider.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("audio response body is required")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if err := emit(provider.ResponseEvent{Kind: provider.EventTextDelta, Text: string(body)}); err != nil {
		return err
	}
	return emit(provider.ResponseEvent{Kind: provider.EventResponseComplete})
}
