package kernel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type RequestCodec interface {
	ID() string
	DescribeCompatibility(CompatibilityContext) []FacetMapping
	Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error)
}

type ResponseDecoder interface {
	ID() string
	PossibleEvents() []ResponseEventKind
	ClassifyError(status int, body []byte) ErrorClass
	Decode(context.Context, UpstreamResponse, func(ResponseEvent) error, StreamHooks) error
}

type ResponseRenderContext struct {
	Status                  int
	Headers                 http.Header
	ClientFormat            normalize.Format
	ProviderFormat          normalize.Format
	Streaming               bool
	ProviderStreaming       bool
	SemanticTransformActive bool
	ProviderEvents          []ResponseEventKind
	RequiredEvents          []ResponseEventKind
	Model                   string
}

type ResponseRenderSession interface {
	Emit(context.Context, ResponseEvent) error
	Finish(context.Context, error) error
}

type ResponseRenderer interface {
	ID() normalize.Format
	SupportsResponse(ResponseRenderContext) CompatibilityPlan
	Begin(context.Context, ResponseRenderContext, http.ResponseWriter) (ResponseRenderSession, error)
}

func responseWriterOutputStarted(writer http.ResponseWriter) bool {
	if committed, ok := writer.(interface{ Committed() bool }); ok {
		return committed.Committed()
	}
	// A renderer with an untracked writer cannot prove that Begin/Emit has not
	// already exposed client bytes, so safe fail-open is unavailable.
	return true
}

type Transport interface {
	ID() string
	Execute(context.Context, UpstreamRequest) (UpstreamResponse, error)
}

type Endpoint interface {
	ID() string
	Resolve(baseURL, operationPath string, options EndpointOptions) (string, error)
}

type EndpointOptions struct {
	Path  string            `json:"path,omitempty"`
	Query map[string]string `json:"query,omitempty"`
}

// HTTPJSONEndpoint combines a provider's API root with the relative path
// selected by its request/model codec. Transport remains a separate primitive.
type HTTPJSONEndpoint struct{}

func (HTTPJSONEndpoint) ID() string { return "http-json" }
func (HTTPJSONEndpoint) Resolve(baseURL, operationPath string, options EndpointOptions) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("invalid HTTP endpoint base URL %q", baseURL)
	}
	if options.Path != "" {
		operationPath = options.Path
	}
	operation, err := url.Parse(strings.TrimSpace(operationPath))
	if err != nil || operation.IsAbs() || operation.Host != "" || operation.Fragment != "" {
		return "", fmt.Errorf("invalid relative endpoint path %q", operationPath)
	}
	escapedPath := strings.TrimRight(base.EscapedPath(), "/") + "/" + strings.TrimLeft(operation.EscapedPath(), "/")
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("invalid escaped endpoint path %q: %w", escapedPath, err)
	}
	base.Path = decodedPath
	canonicalPath := (&url.URL{Path: decodedPath}).EscapedPath()
	if canonicalPath == escapedPath {
		base.RawPath = ""
	} else {
		base.RawPath = escapedPath
	}
	if operation.RawQuery != "" {
		if base.RawQuery != "" {
			base.RawQuery += "&"
		}
		base.RawQuery += operation.RawQuery
	}
	query := base.Query()
	for key, value := range options.Query {
		query.Set(key, value)
	}
	if len(options.Query) > 0 {
		base.RawQuery = query.Encode()
	}
	return base.String(), nil
}

type ComposedAdapter struct {
	AdapterID       string
	Endpoint        Endpoint
	EndpointOptions EndpointOptions
	Request         RequestCodec
	Transport       Transport
	Response        ResponseDecoder
	Renderers       map[normalize.Format]ResponseRenderer
	ProviderFormat  normalize.Format
}

func (a ComposedAdapter) ID() string { return a.AdapterID }
func (a ComposedAdapter) PlanCompatibility(input CompatibilityContext) CompatibilityPlan {
	return cloneCompatibilityPlanContext(a.planCompatibility(input), input)
}

func (a ComposedAdapter) planCompatibility(input CompatibilityContext) CompatibilityPlan {
	format := input.Request.SourceFormat
	streaming := input.Request.Stream
	renderer, ok := a.Renderers[format]
	if !ok {
		requestReport := a.Request.DescribeCompatibility(input)
		responseReport := []FacetMapping{{Facet: FacetWireResponse, Paths: []string{"response"}, Disposition: FacetUnsupported, Reason: "no client renderer is registered for the requested format"}}
		return ComposeCompatibilityPlan([][]FacetMapping{requestReport, responseReport}, withRequiredFacet(input.Policy, FacetWireResponse))
	}
	providerEvents := a.Response.PossibleEvents()
	requiredEvents := RequiredResponseEvents(input.Request)
	if input.ArtifactOutputRequired {
		requiredEvents = uniqueEventKinds(append(requiredEvents, EventArtifact))
	}
	decoderMappings := responseEventMappings(providerEvents)
	declaredEvents := make(map[ResponseEventKind]bool, len(providerEvents))
	for _, event := range providerEvents {
		declaredEvents[event] = true
	}
	for _, event := range requiredEvents {
		if !declaredEvents[event] {
			decoderMappings = append(decoderMappings, FacetMapping{
				Facet: ResponseEventFacet(event), Paths: []string{"upstream.response.events"}, Disposition: FacetUnsupported,
				Reason: fmt.Sprintf("response decoder %q does not declare required event %q", a.Response.ID(), event),
			})
		}
	}
	responseContext := ResponseRenderContext{
		ClientFormat: format, ProviderFormat: a.ProviderFormat, Streaming: streaming, ProviderStreaming: streaming,
		SemanticTransformActive: input.ActiveResponseTransform, ProviderEvents: providerEvents, RequiredEvents: requiredEvents,
	}
	decision := renderer.SupportsResponse(responseContext)
	policy := input.Policy
	policy.RequiredFacets = append(policy.RequiredFacets, FacetWireResponse)
	for _, event := range requiredEvents {
		policy.RequiredFacets = append(policy.RequiredFacets, ResponseEventFacet(event))
	}
	if !decision.Supported {
		responseReport := []FacetMapping{{Facet: FacetWireResponse, Paths: []string{"response"}, Disposition: FacetUnsupported, Reason: decision.Reason}}
		for _, event := range requiredEvents {
			responseReport = append(responseReport, FacetMapping{Facet: ResponseEventFacet(event), Paths: []string{"response.events"}, Disposition: FacetUnsupported, Reason: decision.Reason})
		}
		return ComposeCompatibilityPlan([][]FacetMapping{a.Request.DescribeCompatibility(input), decoderMappings, responseReport}, policy)
	}
	disposition := FacetTranslated
	if decision.Fidelity == FidelityNative {
		disposition = FacetPreserved
	}
	if decision.Fidelity == FidelityLossy || len(decision.Losses) > 0 {
		disposition = FacetDegraded
	}
	mapping := FacetMapping{Facet: FacetWireResponse, Paths: []string{"response"}, Disposition: disposition, Losses: cloneLossRecords(decision.Losses), Reason: decision.Reason}
	responseReport := []FacetMapping{mapping}
	if len(decision.Mappings) > 0 {
		responseReport = append([]FacetMapping(nil), decision.Mappings...)
	}
	declared := make(map[string]bool, len(responseReport))
	for _, item := range responseReport {
		declared[item.Facet] = true
	}
	for _, event := range requiredEvents {
		facet := ResponseEventFacet(event)
		if !declared[facet] {
			responseReport = append(responseReport, FacetMapping{Facet: facet, Paths: []string{"response.events"}, Disposition: FacetUnknown, Reason: "response renderer has no declaration for this required event"})
		}
	}
	return ComposeCompatibilityPlan([][]FacetMapping{a.Request.DescribeCompatibility(input), decoderMappings, responseReport}, policy)
}

func withRequiredFacet(policy CompatibilityPolicy, facet string) CompatibilityPolicy {
	policy.RequiredFacets = append(append([]string(nil), policy.RequiredFacets...), facet)
	return policy
}

func (a ComposedAdapter) Prepare(ctx context.Context, req NormalizedRequest, route Route, credential Credential) (UpstreamRequest, error) {
	upstream, err := a.Request.Prepare(ctx, req, route, credential)
	if err != nil {
		return UpstreamRequest{}, err
	}
	if a.Endpoint == nil {
		return UpstreamRequest{}, fmt.Errorf("adapter %q has no endpoint primitive", a.AdapterID)
	}
	upstream.URL, err = a.Endpoint.Resolve(route.BaseURL, upstream.URL, a.EndpointOptions)
	if err != nil {
		return UpstreamRequest{}, fmt.Errorf("resolve endpoint %q: %w", a.Endpoint.ID(), err)
	}
	return upstream, nil
}
func (a ComposedAdapter) Execute(ctx context.Context, req UpstreamRequest) (UpstreamResponse, error) {
	return a.Transport.Execute(ctx, req)
}
func (a ComposedAdapter) ClassifyError(status int, body []byte) ErrorClass {
	return a.Response.ClassifyError(status, body)
}
func (a ComposedAdapter) RenderResponse(ctx context.Context, response UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks StreamHooks) error {
	renderer := a.Renderers[source]
	if renderer == nil {
		return fmt.Errorf("adapter %q has no renderer for client format %q", a.AdapterID, source)
	}
	rendererCtx := ctx
	if hooks.ArtifactAccess != nil {
		rendererCtx = artifacts.WithAccess(ctx, *hooks.ArtifactAccess)
	}
	providerStreaming := strings.Contains(strings.ToLower(response.Headers.Get("content-type")), "text/event-stream")
	session, err := renderer.Begin(rendererCtx, ResponseRenderContext{
		Status: response.Status, Headers: response.Headers, ClientFormat: source, ProviderFormat: a.ProviderFormat,
		Streaming: hooks.Streaming, ProviderStreaming: providerStreaming, SemanticTransformActive: hooks.TransformResponse != nil, Model: hooks.Model,
	}, writer)
	if err != nil {
		return err
	}
	decodeHooks := hooks
	decodeHooks.OnEvent = nil
	decodeHooks.OnError = nil
	decodeHooks.TransformResponse = nil
	firstByte := false
	signalFirstByte := func(at time.Time) {
		if firstByte {
			return
		}
		firstByte = true
		if hooks.OnFirstByte != nil {
			hooks.OnFirstByte(at)
		}
	}
	decodeHooks.OnFirstByte = signalFirstByte
	decoderCompleted := false
	completionUsage := UsageEvent{}
	decodeHooks.OnComplete = func(event UsageEvent) {
		decoderCompleted = true
		completionUsage = event
	}
	providerComplete := false
	firstEvent := false
	responseOutputStarted := responseWriterOutputStarted(writer)
	var totalEventBytes int64
	emit := func(event ResponseEvent) error {
		if event.Kind == EventResponseComplete {
			providerComplete = true
			if event.Usage != nil {
				completionUsage = *event.Usage
			}
		}
		if event.Kind == EventUsage && event.Usage != nil {
			completionUsage = *event.Usage
		}
		if !firstEvent {
			firstEvent = true
			signalFirstByte(time.Now())
		}
		if hooks.TransformResponse != nil {
			transformCtx := ctx
			if responseOutputStarted || responseWriterOutputStarted(writer) {
				transformCtx = withResponseOutputStarted(ctx)
			}
			transformed, transformErr := hooks.TransformResponse(transformCtx, event)
			if transformErr != nil {
				return transformErr
			}
			event = transformed
		}
		if hooks.MaxEventBytes > 0 {
			eventBytes := int64(len(event.Raw)) + int64(len(event.Opaque)) + int64(len(event.Text)) + int64(len(event.Signature)) + int64(len(event.ToolCallID)) + int64(len(event.ToolName)) + int64(len(event.ToolArguments)) + int64(len(event.Error)) + int64(len(event.ResponseID)) + int64(len(event.ItemID)) + int64(len(event.ContentType)) + int64(len(event.BlockType)) + int64(len(event.StopReason))
			artifactBytes := int64(0)
			for _, artifact := range event.Artifacts {
				eventBytes += int64(len(artifact.TypeRef.ID)+len(artifact.Role)+len(artifact.MediaType)+len(artifact.Sensitivity)+len(artifact.Value)) + 64
				if artifact.Body != nil {
					artifactBytes += artifact.SizeBytes
				}
			}
			if eventBytes > hooks.MaxEventBytes {
				return fmt.Errorf("response event %q exceeds %d-byte operation buffer limit", event.Kind, hooks.MaxEventBytes)
			}
			totalEventBytes += eventBytes + artifactBytes
			if hooks.MaxOutputBytes > 0 && totalEventBytes > hooks.MaxOutputBytes {
				return fmt.Errorf("decoded response exceeds %d-byte operation output limit", hooks.MaxOutputBytes)
			}
		}
		if hooks.ValidateEvent != nil {
			if err := hooks.ValidateEvent(ctx, event); err != nil {
				return fmt.Errorf("validate operation response event %q: %w", event.Kind, err)
			}
		}
		if hooks.OnEvent != nil {
			hooks.OnEvent(event)
		}
		if err := session.Emit(rendererCtx, event); err != nil {
			return err
		}
		responseOutputStarted = responseWriterOutputStarted(writer)
		return nil
	}
	decodeErr := a.Response.Decode(ctx, response, emit, decodeHooks)
	if decodeErr == nil && !providerComplete {
		decodeErr = io.ErrUnexpectedEOF
	}
	if decodeErr != nil {
		_ = emit(ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: decodeErr.Error()})
	}
	finishErr := session.Finish(rendererCtx, decodeErr)
	if decodeErr != nil {
		if hooks.OnError != nil {
			hooks.OnError(decodeErr)
		}
		return decodeErr
	}
	if finishErr != nil {
		if hooks.OnError != nil {
			hooks.OnError(finishErr)
		}
		return finishErr
	}
	if !decoderCompleted {
		completionUsage.Status = "ok"
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(completionUsage)
	}
	return nil
}

type HTTPTransport struct {
	Client  *http.Client
	Timeout time.Duration
}

func (t HTTPTransport) ID() string { return "http" }
func (t HTTPTransport) Execute(ctx context.Context, request UpstreamRequest) (UpstreamResponse, error) {
	client := t.Client
	if client == nil {
		timeout := t.Timeout
		if timeout <= 0 {
			timeout = 2 * time.Minute
		}
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, request.Body)
	if err != nil {
		return UpstreamResponse{}, err
	}
	req.Header = request.Headers
	response, err := client.Do(req)
	if err != nil {
		return UpstreamResponse{}, err
	}
	return UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}
