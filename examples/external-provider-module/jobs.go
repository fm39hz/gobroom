package externalprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
)

const jobClientFormat provider.Format = "example.jobs.client"

var jobArtifact = provider.Ref{Kind: provider.ArtifactKind, ID: "example.jobs.handle", ContractVersion: 1}
var jobResultArtifact = provider.Ref{Kind: provider.ArtifactKind, ID: "example.jobs.result", ContractVersion: 1}
var jobProvider = provider.Ref{Kind: "provider-definition", ID: "example-jobs", ContractVersion: 1}

// JobModule composes upstream async effects from ordinary operations. It owns
// no daemon worker or job database: the upstream owns the job's lifecycle.
type JobModule struct{}

func (JobModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.jobs", Version: "2.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func jobOperation(action string) provider.Ref {
	return provider.Ref{Kind: "operation", ID: "example.jobs." + action, ContractVersion: 2}
}

func (JobModule) Register(runtime *provider.RuntimeRegistry) error {
	handleSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.handle.value", ContractVersion: 1}
	resultArtifactSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.result.value", ContractVersion: 1}
	logicalResultSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.result", ContractVersion: 2}
	for _, schema := range []struct {
		ref   provider.Ref
		value json.RawMessage
	}{
		{handleSchema, json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"}},"required":["id"],"additionalProperties":false}`)},
		{resultArtifactSchema, json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":128},"status":{"enum":["pending","running","completed","cancelled"]}},"required":["id","status"],"additionalProperties":false}`)},
		{logicalResultSchema, json.RawMessage(`{"type":"object","properties":{"version":{"const":2},"typeRef":{"type":"object","properties":{"kind":{"const":"artifact"},"id":{"const":"example.jobs.result"},"contractVersion":{"const":1}},"required":["kind","id","contractVersion"],"additionalProperties":false},"role":{"const":"result"},"mediaType":{"const":"application/json"},"sizeBytes":{"type":"integer","minimum":1,"maximum":4096}},"required":["version","typeRef","role","mediaType","sizeBytes"],"additionalProperties":false}`)},
	} {
		if err := runtime.RegisterSchemaDocument(schema.ref, schema.value); err != nil {
			return err
		}
	}
	if err := runtime.RegisterExtension(provider.ExtensionDescriptor{
		Ref: jobArtifact, ImplementationVersion: "1", DisplayName: "Upstream job handle", Description: "A bounded handle retained by the client between operations.", InputSchemaRef: &handleSchema,
		ResourceBounds: provider.ResourceBounds{MaxInputBytes: 512},
		ArtifactPolicy: &provider.ArtifactPolicy{OwnerDomains: []string{"provider"}, ReplayScopes: []provider.ArtifactReplayScope{provider.ArtifactReplayConnection}, SensitivityLimit: provider.ArtifactPrivate, AllowedMediaTypes: []string{"application/json"}, RecipientContracts: []provider.Ref{jobProvider, jobOperation("create")}},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		return err
	}
	if err := runtime.RegisterExtension(provider.ExtensionDescriptor{
		Ref: jobResultArtifact, ImplementationVersion: "1", DisplayName: "Upstream job result", Description: "The bounded result returned by one upstream job action.", InputSchemaRef: &resultArtifactSchema,
		ResourceBounds: provider.ResourceBounds{MaxInputBytes: 4096},
		ArtifactPolicy: &provider.ArtifactPolicy{OwnerDomains: []string{"provider"}, ReplayScopes: []provider.ArtifactReplayScope{provider.ArtifactReplayRequest}, SensitivityLimit: provider.ArtifactPrivate, AllowedMediaTypes: []string{"application/json"}, RecipientContracts: []provider.Ref{jobOperation("create"), jobOperation("status"), jobOperation("cancel")}},
	}, func(json.RawMessage) (any, error) { return struct{}{}, nil }); err != nil {
		return err
	}
	for _, action := range []string{"create", "status", "cancel"} {
		ref := jobOperation(action)
		inputRef := provider.Ref{Kind: provider.SchemaKind, ID: ref.ID + ".input", ContractVersion: 1}
		input := json.RawMessage(`{"type":"object","additionalProperties":false}`)
		if action == "create" {
			input = json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":1024}},"required":["prompt"],"additionalProperties":false}`)
		}
		if err := runtime.RegisterSchemaDocument(inputRef, input); err != nil {
			return err
		}
		definition := provider.OperationDefinition{
			Ref: ref, DisplayName: "Job " + action, Description: "Perform one upstream job action.", InputSchemaRef: inputRef, ResultSchemaRef: &logicalResultSchema,
			ResourceBounds: provider.ResourceBounds{MaxInputBytes: 4096, MaxOutputBytes: 4096, MaxBufferedBytes: 4096, DeadlineMillis: 5000}, ReplaySafety: provider.ReplayNever,
			InputPayload:        func(request provider.OperationRequest) (json.RawMessage, error) { return request.OperationPayload, nil },
			CompileRequirements: func(provider.OperationRequest) ([]provider.FeatureRequirement, error) { return nil, nil },
			NewResultProjector: func(bounds provider.ResourceBounds) (provider.ResultProjector, error) {
				return &jobResultProjector{max: bounds.MaxBufferedBytes}, nil
			},
		}
		definition.ArtifactOutputs = []provider.ArtifactOutput{{Role: "result", TypeRef: jobResultArtifact, MinCount: 1, MaxCount: 1}}
		if action == "create" {
			definition.ReplaySafety = provider.ReplayWithKey
			definition.ArtifactOutputs = append(definition.ArtifactOutputs, provider.ArtifactOutput{Role: "job", TypeRef: jobArtifact, MinCount: 1, MaxCount: 1})
		} else {
			definition.ArtifactInputs = []provider.ArtifactInput{{Role: "job", TypeRef: jobArtifact, MinCount: 1, MaxCount: 1}}
		}
		if err := runtime.Primitives.Operations.Register(definition); err != nil {
			return err
		}
		if err := runtime.RegisterRequestCodec(jobRequestCodec{action: action}); err != nil {
			return err
		}
		if err := runtime.RegisterResponseDecoderVersion(jobResponseDecoder{action: action}, 2); err != nil {
			return err
		}
		if err := runtime.RegisterClientIngress(jobIngress{provider.JSONOperationIngress{CodecID: ref.ID + ".json", IngressContractVersion: 1, Operation: ref.ID, OperationContractVersion: ref.ContractVersion, Endpoints: []provider.ClientIngressRoute{{Method: http.MethodPost, Path: "/v1/jobs/" + action}}}}); err != nil {
			return err
		}
	}
	return runtime.RegisterResponseRenderer(jobRenderer{})
}

type jobIngress struct{ provider.JSONOperationIngress }

func (c jobIngress) Decode(request *http.Request) (provider.NormalizedRequest, error) {
	decoded, err := c.JSONOperationIngress.Decode(request)
	decoded.SourceFormat = jobClientFormat
	return decoded, err
}

type jobHandle struct {
	ID string `json:"id"`
}
type jobResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type jobLogicalResult struct {
	Version   int          `json:"version"`
	TypeRef   provider.Ref `json:"typeRef"`
	Role      string       `json:"role"`
	MediaType string       `json:"mediaType"`
	SizeBytes int64        `json:"sizeBytes"`
}

type jobRequestCodec struct{ action string }

func (c jobRequestCodec) ID() string { return "example.jobs." + c.action }
func (c jobRequestCodec) DescribeCompatibility(input provider.CompatibilityContext) []provider.FacetMapping {
	var mappings []provider.FacetMapping
	for _, facet := range provider.RequiredRequestFacets(input.Request) {
		mappings = append(mappings, provider.FacetMapping{Facet: facet, Paths: []string{"operationPayload", "artifacts"}, Disposition: provider.FacetTranslated})
	}
	return mappings
}
func (c jobRequestCodec) Prepare(ctx context.Context, request provider.NormalizedRequest, route provider.Route, credential provider.Credential) (provider.UpstreamRequest, error) {
	method, path := http.MethodPost, "/jobs"
	var body []byte
	if c.action == "create" {
		var input struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(request.OperationPayload, &input); err != nil {
			return provider.UpstreamRequest{}, err
		}
		var err error
		body, err = json.Marshal(struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}{route.ExternalModel, input.Prompt})
		if err != nil {
			return provider.UpstreamRequest{}, err
		}
	} else {
		if len(request.Artifacts) != 1 {
			return provider.UpstreamRequest{}, fmt.Errorf("job action requires one scoped handle")
		}
		var handle jobHandle
		if err := json.Unmarshal(request.Artifacts[0].Value, &handle); err != nil {
			return provider.UpstreamRequest{}, err
		}
		path = "/jobs/" + url.PathEscape(handle.ID)
		if c.action == "status" {
			method = http.MethodGet
		} else {
			path += "/cancel"
		}
	}
	return provider.PrepareBytesRequest(ctx, method, path, http.Header{"Content-Type": {"application/json"}}, body, credential, provider.CredentialAuthorization(route.CredentialPresentation), request.Transport.IdempotencyKey != "")
}

type jobResponseDecoder struct{ action string }

func (c jobResponseDecoder) ID() string { return "example.jobs." + c.action }
func (c jobResponseDecoder) PossibleEvents() []provider.ResponseEventKind {
	return []provider.ResponseEventKind{provider.EventArtifact, provider.EventResponseComplete}
}
func (jobResponseDecoder) ClassifyError(int, []byte) provider.ErrorClass { return "retryable" }
func (c jobResponseDecoder) Decode(ctx context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, _ provider.StreamHooks) error {
	defer response.Body.Close()
	var result jobResult
	wire, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return err
	}
	if len(wire) > 4096 {
		return fmt.Errorf("job response exceeds its decode bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("job response must contain exactly one JSON envelope")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	resultArtifact, err := provider.PublishInlineResponseArtifact(ctx, jobResultArtifact, "result", "application/json", provider.ArtifactReplayRequest, provider.ArtifactPrivate, encoded, 5*time.Minute)
	if err != nil {
		return err
	}
	artifacts := []provider.ArtifactRef{resultArtifact}
	if c.action == "create" {
		value, err := json.Marshal(jobHandle{ID: result.ID})
		if err != nil {
			return err
		}
		artifact, err := provider.PublishInlineResponseArtifact(ctx, jobArtifact, "job", "application/json", provider.ArtifactReplayConnection, provider.ArtifactPrivate, value, 5*time.Minute)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
	}
	if err := emit(provider.ResponseEvent{Kind: provider.EventArtifact, Artifacts: artifacts}); err != nil {
		return err
	}
	return emit(provider.ResponseEvent{Kind: provider.EventResponseComplete})
}

type jobResultProjector struct {
	max        int64
	result     json.RawMessage
	resultSeen bool
}

func (p *jobResultProjector) ConsumeEvent(raw json.RawMessage) error {
	var event struct {
		Kind    string `json:"kind"`
		Content struct {
			Artifacts []struct {
				TypeRef   provider.Ref `json:"typeRef"`
				Role      string       `json:"role"`
				MediaType string       `json:"mediaType"`
				SizeBytes int64        `json:"sizeBytes"`
			} `json:"artifacts"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.Kind != string(provider.EventArtifact) {
		return nil
	}
	for _, artifact := range event.Content.Artifacts {
		if artifact.TypeRef != jobResultArtifact || artifact.Role != "result" {
			continue
		}
		if p.resultSeen || artifact.MediaType != "application/json" || artifact.SizeBytes <= 0 || artifact.SizeBytes > p.max {
			return fmt.Errorf("job result artifact is duplicated or exceeds projector bounds")
		}
		p.resultSeen = true
		logical := jobLogicalResult{Version: 2, TypeRef: jobResultArtifact, Role: "result", MediaType: "application/json", SizeBytes: artifact.SizeBytes}
		encoded, err := json.Marshal(logical)
		if err != nil {
			return err
		}
		p.result = encoded
	}
	return nil
}
func (p *jobResultProjector) Finalize() (json.RawMessage, error) {
	if !p.resultSeen {
		return nil, fmt.Errorf("job result artifact is missing")
	}
	return p.result, nil
}

type jobRenderer struct{}

func (jobRenderer) ID() provider.Format { return jobClientFormat }
func (jobRenderer) SupportsResponse(options provider.ResponseRenderContext) provider.CompatibilityPlan {
	mappings := []provider.FacetMapping{{Facet: provider.FacetWireResponse, Paths: []string{"job.json"}, Disposition: provider.FacetTranslated}}
	required := []string{provider.FacetWireResponse}
	for _, event := range options.ProviderEvents {
		mappings = append(mappings, provider.FacetMapping{Facet: provider.ResponseEventFacet(event), Paths: []string{"job.json"}, Disposition: provider.FacetPreserved})
	}
	for _, event := range options.RequiredEvents {
		required = append(required, provider.ResponseEventFacet(event))
	}
	return provider.ComposeCompatibilityPlan([][]provider.FacetMapping{mappings}, provider.CompatibilityPolicy{RequiredFacets: required})
}
func (jobRenderer) Begin(_ context.Context, _ provider.ResponseRenderContext, writer http.ResponseWriter) (provider.ResponseRenderSession, error) {
	return &jobRenderSession{writer: writer}, nil
}

type jobEnvelope struct {
	Result    jobResult              `json:"result"`
	Artifacts []provider.ArtifactRef `json:"artifacts,omitempty"`
}
type jobRenderSession struct {
	writer     http.ResponseWriter
	envelope   jobEnvelope
	resultSeen bool
	handleSeen bool
}

func (s *jobRenderSession) Emit(_ context.Context, event provider.ResponseEvent) error {
	switch event.Kind {
	case provider.EventArtifact:
		for _, artifact := range event.Artifacts {
			switch {
			case artifact.TypeRef == jobResultArtifact && artifact.Role == "result":
				if s.resultSeen || artifact.MediaType != "application/json" || len(artifact.Value) == 0 || int64(len(artifact.Value)) != artifact.SizeBytes {
					return fmt.Errorf("job response has a duplicate or malformed result artifact")
				}
				var result jobResult
				if err := decodeJobResult(artifact.Value, &result); err != nil {
					return err
				}
				s.envelope.Result = result
				s.resultSeen = true
			case artifact.TypeRef == jobArtifact && artifact.Role == "job":
				if s.handleSeen {
					return fmt.Errorf("job response has duplicate job handles")
				}
				s.handleSeen = true
				s.envelope.Artifacts = append(s.envelope.Artifacts, artifact.Clone())
			default:
				return fmt.Errorf("job response contains an undeclared artifact")
			}
		}
	case provider.EventTextDelta:
		return fmt.Errorf("job response unexpectedly contains text events")
	}
	return nil
}
func (s *jobRenderSession) Finish(_ context.Context, err error) error {
	if err != nil {
		return err
	}
	if !s.resultSeen {
		return fmt.Errorf("job response is missing its result artifact")
	}
	s.writer.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(s.writer).Encode(s.envelope)
}

func decodeJobResult(value []byte, result *jobResult) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("job result artifact must contain exactly one JSON object")
	}
	return nil
}
