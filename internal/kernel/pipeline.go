package kernel

import (
	"context"
	"errors"
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

// PlannedRequestCodec may consume the exact admitted compatibility plan when
// preparing provider wire data. It is used for explicitly approved lossy
// mappings; codecs that do not need plan evidence keep the simpler contract.
type PlannedRequestCodec interface {
	PrepareWithCompatibility(context.Context, NormalizedRequest, Route, Credential, CompatibilityPlan) (UpstreamRequest, error)
}

// CompatibilityPlanPreparer lets the kernel pass the candidate-specific plan
// to composed adapters without coupling the kernel to any provider codec.
type CompatibilityPlanPreparer interface {
	PrepareWithCompatibility(context.Context, NormalizedRequest, Route, Credential, CompatibilityPlan) (UpstreamRequest, error)
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
	IncludeUsage            bool
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
		IncludeUsage:            input.Request.Transport.IncludeUsage,
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
	return a.PrepareWithCompatibility(ctx, req, route, credential, CompatibilityPlan{})
}
func (a ComposedAdapter) PrepareWithCompatibility(ctx context.Context, req NormalizedRequest, route Route, credential Credential, plan CompatibilityPlan) (UpstreamRequest, error) {
	var upstream UpstreamRequest
	var err error
	if planned, ok := a.Request.(PlannedRequestCodec); ok {
		upstream, err = planned.PrepareWithCompatibility(ctx, req, route, credential, plan)
	} else {
		upstream, err = a.Request.Prepare(ctx, req, route, credential)
	}
	if err != nil {
		if body, ok := upstream.Body.(io.Closer); ok {
			_ = body.Close()
		}
		return UpstreamRequest{}, err
	}
	if a.Endpoint == nil {
		return UpstreamRequest{}, fmt.Errorf("adapter %q has no endpoint primitive", a.AdapterID)
	}
	encodedURL := upstream.URL
	upstream.URL, err = a.Endpoint.Resolve(route.BaseURL, encodedURL, a.EndpointOptions)
	if err != nil {
		if body, ok := upstream.Body.(io.Closer); ok {
			_ = body.Close()
		}
		return UpstreamRequest{}, fmt.Errorf("resolve endpoint %q: %w", a.Endpoint.ID(), err)
	}
	if replay := upstream.Replay; replay != nil {
		resolvedURL, method := upstream.URL, upstream.Method
		var wrapped PreparedRequestReplay
		wrapped = func(ctx context.Context, credential Credential) (UpstreamRequest, error) {
			next, err := replay(ctx, credential)
			if err != nil {
				return next, err
			}
			if next.URL != encodedURL || next.Method != method {
				if body, ok := next.Body.(io.Closer); ok {
					_ = body.Close()
				}
				return UpstreamRequest{}, fmt.Errorf("prepared replay changed its encoded action")
			}
			next.URL = resolvedURL
			next.Replay = wrapped
			return next, nil
		}
		upstream.Replay = wrapped
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
	if response.Body != nil {
		lease := &responseBodyLease{ReadCloser: response.Body}
		response.Body = lease
		defer lease.Close()
	}
	fail := func(origin ResponseFailureOrigin, err error) error {
		err = WrapResponseFailure(origin, err)
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return fail(ResponseFailureRenderer, err)
	}
	renderer := a.Renderers[source]
	if renderer == nil {
		return fail(ResponseFailureRenderer, fmt.Errorf("adapter %q has no renderer for client format %q", a.AdapterID, source))
	}
	rendererCtx := ctx
	if hooks.ArtifactAccess != nil {
		rendererCtx = artifacts.WithAccess(ctx, *hooks.ArtifactAccess)
	}
	providerStreaming := strings.Contains(strings.ToLower(response.Headers.Get("content-type")), "text/event-stream")
	session, err := renderer.Begin(rendererCtx, ResponseRenderContext{
		Status: response.Status, Headers: response.Headers, ClientFormat: source, ProviderFormat: a.ProviderFormat,
		Streaming: hooks.Streaming, ProviderStreaming: providerStreaming, SemanticTransformActive: hooks.TransformResponse != nil, Model: hooks.Model,
		IncludeUsage: hooks.IncludeUsage,
	}, writer)
	if err != nil {
		return fail(ResponseFailureRenderer, err)
	}
	decodeHooks := hooks
	decodeHooks.OnEvent = nil
	decodeHooks.OnError = nil
	decodeHooks.NormalizeProviderFailure = nil
	decodeHooks.TransformResponse = nil
	decodeHooks.CompleteUsage = nil
	baseAdmission := hooks.AdmitUsage
	if baseAdmission == nil {
		baseAdmission = func(event UsageEvent) UsageEvent { return event.SanitizeCommon() }
	}
	admitUsage := func(event UsageEvent) UsageEvent {
		raw := event.RawUsage
		event.RawUsage = nil
		if raw != nil && raw.ProviderDefinition != hooks.ProviderIssuer {
			event.AccountingIssues = append(event.AccountingIssues, UsageIssue{Code: "raw_usage_issuer_mismatch"})
		} else if raw != nil {
			selected, issues := SelectRawUsage(hooks.UsageCapture, raw.ProviderDefinition, raw.API, raw.Value)
			event.RawUsage = selected
			event.AccountingIssues = append(event.AccountingIssues, issues...)
		}
		return baseAdmission(event)
	}
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
		event = admitUsage(event)
		decoderCompleted = true
		completionUsage = admitUsage(MergeProviderUsage(completionUsage, event))
		if hooks.OnProviderUsage != nil {
			hooks.OnProviderUsage(event.Clone())
		}
	}
	decodeHooks.OnProviderUsage = func(event UsageEvent) {
		event = admitUsage(event)
		completionUsage = admitUsage(MergeProviderUsage(completionUsage, event))
		if hooks.OnProviderUsage != nil {
			hooks.OnProviderUsage(event.Clone())
		}
	}
	privateSession := &sessionUpdates{ctx: ctx}
	decodeHooks.OnSessionState = privateSession.capture
	providerComplete := false
	firstEvent := false
	responseOutputStarted := responseWriterOutputStarted(writer)
	providerBudget := responseEventBudget{maxEvent: hooks.MaxEventBytes, maxOutput: hooks.MaxOutputBytes}
	clientBudget := responseEventBudget{maxEvent: hooks.MaxEventBytes, maxOutput: hooks.MaxOutputBytes}
	var emitFailure error
	var pendingCompletion *ResponseEvent
	emitOne := func(event ResponseEvent, providerBudgeted bool) error {
		if event.Usage != nil {
			usage := admitUsage(*event.Usage)
			event.Usage = &usage
		}
		if event.Usage != nil && (event.Kind == EventUsage || event.Kind == EventResponseComplete) && hooks.OnProviderUsage != nil {
			hooks.OnProviderUsage(event.Usage.Clone())
		}
		if emitFailure != nil {
			return emitFailure
		}
		if err := ctx.Err(); err != nil {
			return WrapResponseFailure(ResponseFailureProvider, err)
		}
		if !providerBudgeted {
			if err := providerBudget.consume(event); err != nil {
				return WrapResponseFailure(ResponseFailureProvider, err)
			}
		}
		if event.Kind == EventThinkingSignature && event.SignatureIssuer.ID == "" {
			event.SignatureIssuer = hooks.ProviderIssuer
		}
		if event.Kind == EventResponseComplete {
			providerComplete = true
			if event.Usage != nil {
				completionUsage = admitUsage(MergeProviderUsage(completionUsage, *event.Usage))
			}
			if hooks.CompleteUsage != nil {
				finalUsage, err := hooks.CompleteUsage(ctx, completionUsage)
				if err != nil {
					return err
				}
				completionUsage = finalUsage.Clone()
			}
		}
		if event.Kind == EventUsage && event.Usage != nil {
			completionUsage = admitUsage(MergeProviderUsage(completionUsage, *event.Usage))
		}
		if event.Usage != nil {
			clientUsage := *event.Usage
			clientUsage.RawUsage = nil
			clientUsage = clientUsage.Clone()
			event.Usage = &clientUsage
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
				return WrapResponseFailure(ResponseFailureTransform, transformErr)
			}
			event = transformed
			if err := clientBudget.consume(event); err != nil {
				return WrapResponseFailure(ResponseFailureTransform, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return WrapResponseFailure(ResponseFailureTransform, err)
		}
		if hooks.ValidateEvent != nil {
			if err := hooks.ValidateEvent(ctx, event); err != nil {
				origin := ResponseFailureProvider
				if hooks.TransformResponse != nil {
					origin = ResponseFailureUnknown
				}
				return WrapResponseFailure(origin, fmt.Errorf("validate operation response event %q: %w", event.Kind, err))
			}
		}
		if hooks.OnEvent != nil {
			hooks.OnEvent(event)
		}
		if err := session.Emit(rendererCtx, event); err != nil {
			return WrapResponseFailure(ResponseFailureRenderer, err)
		}
		if err := ctx.Err(); err != nil {
			return WrapResponseFailure(ResponseFailureRenderer, err)
		}
		responseOutputStarted = responseWriterOutputStarted(writer)
		return nil
	}
	emit := func(event ResponseEvent) error {
		// A decoder may report final usage after its semantic completion event
		// (for example an OpenAI usage-only tail). Gate client completion only
		// after Decode has returned, not at the first finish_reason.
		if event.Kind == EventResponseComplete {
			if emitFailure != nil {
				return emitFailure
			}
			if err := ctx.Err(); err != nil {
				emitFailure = WrapResponseFailure(ResponseFailureProvider, err)
				return emitFailure
			}
			if event.Usage != nil {
				usage := admitUsage(*event.Usage)
				event.Usage = &usage
				completionUsage = admitUsage(MergeProviderUsage(completionUsage, usage))
				if hooks.OnProviderUsage != nil {
					hooks.OnProviderUsage(usage.Clone())
				}
			}
			if err := providerBudget.consume(event); err != nil {
				emitFailure = WrapResponseFailure(ResponseFailureProvider, err)
				return emitFailure
			}
			// Multiple provider terminal observations (finish_reason and DONE,
			// for example) still finalize this dispatch exactly once.
			if pendingCompletion != nil {
				if pendingCompletion.ResponseID == "" {
					pendingCompletion.ResponseID = event.ResponseID
				}
				return nil
			}
			owned := cloneResponseEvent(event)
			pendingCompletion = &owned
			providerComplete = true
			return nil
		}
		err := emitOne(event, false)
		if err != nil && emitFailure == nil {
			emitFailure = err
		}
		return err
	}
	decodeErr := a.Response.Decode(ctx, response, emit, decodeHooks)
	if emitFailure != nil {
		decodeErr = emitFailure
	}
	if decodeErr == nil && ctx.Err() != nil {
		decodeErr = ctx.Err()
	}
	if decodeErr == nil && !providerComplete {
		decodeErr = io.ErrUnexpectedEOF
	}
	if decodeErr == nil && pendingCompletion != nil {
		// Accounting is independent of the operation's client payload schema.
		// Refresh a decoder-declared usage field, but never invent one merely
		// because the host has operational accounting or enrichment metadata.
		if pendingCompletion.Usage != nil {
			usage := completionUsage.Clone()
			pendingCompletion.Usage = &usage
		}
		decodeErr = emitOne(*pendingCompletion, true)
		if decodeErr != nil {
			emitFailure = decodeErr
		}
	}
	if sessionErr := privateSession.finish(hooks.OnSessionState, decodeErr == nil); decodeErr == nil && sessionErr != nil {
		decodeErr = sessionErr
	}
	if decodeErr != nil && emitFailure == nil && ctx.Err() == nil && hooks.NormalizeProviderFailure != nil && (responseFailureOrigin(decodeErr) == ResponseFailureUnknown || responseFailureOrigin(decodeErr) == ResponseFailureProvider) {
		var evidence *ProviderResponseError
		if errors.As(decodeErr, &evidence) {
			if !validProviderResponseError(evidence.data) || hooks.MaxEventBytes > 0 && int64(len(evidence.data)) > hooks.MaxEventBytes {
				decodeErr = fmt.Errorf("provider response error evidence exceeds its contract")
			} else if normalized := hooks.NormalizeProviderFailure(evidence); normalized != nil {
				decodeErr = normalized
			}
		}
	}
	if decodeErr != nil {
		decodeErr = WrapResponseFailure(ResponseFailureProvider, decodeErr)
		errorEvent := ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: ClientFailureForError(decodeErr)}
		if hooks.OnEvent != nil {
			hooks.OnEvent(errorEvent)
		}
		if ctx.Err() == nil && responseWriterOutputStarted(writer) {
			// Project an error only onto an already committed client response.
			// Do not re-run the failed transform/validator to render diagnostics.
			_ = session.Emit(rendererCtx, errorEvent)
		}
	}
	finishErr := session.Finish(rendererCtx, decodeErr)
	if decodeErr != nil {
		return fail(ResponseFailureProvider, decodeErr)
	}
	if finishErr != nil {
		return fail(ResponseFailureRenderer, finishErr)
	}
	if err := ctx.Err(); err != nil {
		return fail(ResponseFailureRenderer, err)
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
		if body, ok := request.Body.(io.Closer); ok {
			_ = body.Close()
		}
		return UpstreamResponse{}, err
	}
	req.Header = request.Headers
	// The kernel owns redispatch. Do not let net/http rewind byte readers or
	// treat an empty body plus Idempotency-Key as proof of unlimited replay.
	req.GetBody = nil
	if req.Body == nil || req.Body == http.NoBody {
		req.Body = io.NopCloser(strings.NewReader(""))
	}
	// A redirect may change the issuer/action and invalidate a signature.
	// Copy client settings without mutating the injected/shared client.
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := requestClient.Do(req)
	if err != nil {
		return UpstreamResponse{}, err
	}
	return UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}
