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
var jobProvider = provider.Ref{Kind: "provider-definition", ID: "example-jobs", ContractVersion: 1}

// JobModule composes upstream async effects from ordinary operations. It owns
// no daemon worker or job database: the upstream owns the job's lifecycle.
type JobModule struct{}

func (JobModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.jobs", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func jobOperation(action string) provider.Ref {
	return provider.Ref{Kind: "operation", ID: "example.jobs." + action, ContractVersion: 1}
}

func (JobModule) Register(runtime *provider.RuntimeRegistry) error {
	handleSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.handle.value", ContractVersion: 1}
	resultSchema := provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.result", ContractVersion: 1}
	for _, schema := range []struct {
		ref   provider.Ref
		value json.RawMessage
	}{
		{handleSchema, json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,128}$"}},"required":["id"],"additionalProperties":false}`)},
		{resultSchema, json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","minLength":1},"status":{"enum":["pending","running","completed","cancelled"]}},"required":["id","status"],"additionalProperties":false}`)},
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
			Ref: ref, DisplayName: "Job " + action, Description: "Perform one upstream job action.", InputSchemaRef: inputRef, ResultSchemaRef: &resultSchema,
			ResourceBounds: provider.ResourceBounds{MaxInputBytes: 4096, MaxOutputBytes: 4096, MaxBufferedBytes: 4096, DeadlineMillis: 5000}, ReplaySafety: provider.ReplayNever,
			InputPayload:        func(request provider.OperationRequest) (json.RawMessage, error) { return request.OperationPayload, nil },
			CompileRequirements: func(provider.OperationRequest) ([]provider.FeatureRequirement, error) { return nil, nil },
			NewResultProjector: func(bounds provider.ResourceBounds) (provider.ResultProjector, error) {
				return &jobResultProjector{max: bounds.MaxBufferedBytes}, nil
			},
		}
		if action == "create" {
			definition.ReplaySafety = provider.ReplayWithKey
			definition.ArtifactOutputs = []provider.ArtifactOutput{{Role: "job", TypeRef: jobArtifact, MinCount: 1, MaxCount: 1}}
		} else {
			definition.ArtifactInputs = []provider.ArtifactInput{{Role: "job", TypeRef: jobArtifact, MinCount: 1, MaxCount: 1}}
		}
		if err := runtime.Primitives.Operations.Register(definition); err != nil {
			return err
		}
		if err := runtime.RegisterRequestCodec(jobRequestCodec{action: action}); err != nil {
			return err
		}
		if err := runtime.RegisterResponseDecoder(jobResponseDecoder{action: action}); err != nil {
			return err
		}
		if err := runtime.RegisterClientIngress(jobIngress{provider.JSONOperationIngress{CodecID: ref.ID + ".json", IngressContractVersion: 1, Operation: ref.ID, OperationContractVersion: 1, Endpoints: []provider.ClientIngressRoute{{Method: http.MethodPost, Path: "/v1/jobs/" + action}}}}); err != nil {
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
	events := []provider.ResponseEventKind{provider.EventTextDelta, provider.EventResponseComplete}
	if c.action == "create" {
		events = append(events, provider.EventArtifact)
	}
	return events
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
	if c.action == "create" {
		value, err := json.Marshal(jobHandle{ID: result.ID})
		if err != nil {
			return err
		}
		artifact, err := provider.PublishInlineResponseArtifact(ctx, jobArtifact, "job", "application/json", provider.ArtifactReplayConnection, provider.ArtifactPrivate, value, 5*time.Minute)
		if err != nil {
			return err
		}
		if err := emit(provider.ResponseEvent{Kind: provider.EventArtifact, Artifacts: []provider.ArtifactRef{artifact}}); err != nil {
			return err
		}
	}
	if err := emit(provider.ResponseEvent{Kind: provider.EventTextDelta, Text: string(encoded)}); err != nil {
		return err
	}
	return emit(provider.ResponseEvent{Kind: provider.EventResponseComplete})
}

type jobResultProjector struct {
	max    int64
	result json.RawMessage
}

func (p *jobResultProjector) ConsumeEvent(raw json.RawMessage) error {
	var event struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.Content.Text != "" {
		if p.result != nil || int64(len(event.Content.Text)) > p.max {
			return fmt.Errorf("job result exceeds projector bounds")
		}
		p.result = json.RawMessage(event.Content.Text)
	}
	return nil
}
func (p *jobResultProjector) Finalize() (json.RawMessage, error) { return p.result, nil }

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
	writer   http.ResponseWriter
	envelope jobEnvelope
}

func (s *jobRenderSession) Emit(_ context.Context, event provider.ResponseEvent) error {
	if event.Kind == provider.EventArtifact {
		for _, artifact := range event.Artifacts {
			s.envelope.Artifacts = append(s.envelope.Artifacts, artifact.Clone())
		}
	}
	if event.Kind == provider.EventTextDelta {
		return json.Unmarshal([]byte(event.Text), &s.envelope.Result)
	}
	return nil
}
func (s *jobRenderSession) Finish(_ context.Context, err error) error {
	if err != nil {
		return err
	}
	s.writer.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(s.writer).Encode(s.envelope)
}
