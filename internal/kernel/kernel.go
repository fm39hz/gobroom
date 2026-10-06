package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type Kernel struct {
	Snapshots          *SnapshotStore
	Scheduler          *Scheduler
	Adapters           map[string]ProviderAdapter
	ErrorClassifiers   map[string]ErrorClassifier
	UsageSources       map[string]UsageEnricher
	SessionStores      map[string]SessionStore
	Transforms         *RequestTransformRegistry
	ResponseTransforms *ResponseTransformRegistry
	Features           *FeatureRegistry
	Operations         *operations.Snapshot
	Events             chan UsageEvent
	ResolveCredential  CredentialResolver
	RefreshCredential  CredentialRefresher
	closed             atomic.Bool
}

type FeedbackGate interface {
	Gate
	MarkFailure(Route, ErrorClass, error)
	MarkFailureAfter(Route, ErrorClass, error, time.Duration)
	MarkSuccess(Route)
}

func New(initial Snapshot, gate Gate, buffer int) (*Kernel, error) {
	store, err := NewSnapshotStore(initial)
	if err != nil {
		return nil, err
	}
	if buffer < 1 {
		buffer = 256
	}
	operationSnapshot, err := operations.BuiltinSnapshot()
	if err != nil {
		return nil, fmt.Errorf("build operation catalog: %w", err)
	}
	return &Kernel{Snapshots: store, Scheduler: NewScheduler(gate), Adapters: map[string]ProviderAdapter{}, ErrorClassifiers: map[string]ErrorClassifier{}, UsageSources: map[string]UsageEnricher{}, SessionStores: map[string]SessionStore{}, Features: NewFeatureRegistry(), Operations: operationSnapshot, Transforms: NewRequestTransformRegistry(), ResponseTransforms: NewResponseTransformRegistry(), Events: make(chan UsageEvent, buffer)}, nil
}

func (k *Kernel) transformResponse(ctx context.Context, event ResponseEvent) (ResponseEvent, error) {
	return k.ResponseTransforms.Apply(ctx, event)
}

func (k *Kernel) Resolve(name string) (ResolvedModel, error) {
	return ResolvePublic(k.Snapshots.Load(), name)
}

func (k *Kernel) ResolveNode(name string) (ModelNode, error) {
	return ResolvePublicNode(k.Snapshots.Load(), name)
}

func (k *Kernel) Select(name string, now time.Time) (Route, error) {
	model, err := k.Resolve(name)
	if err != nil {
		return Route{}, err
	}
	return k.Scheduler.Select(model, now)
}

func (k *Kernel) PublishSnapshot(snapshot Snapshot) error { return k.Snapshots.Publish(snapshot) }

func (k *Kernel) EmitUsage(event UsageEvent) bool {
	if k.closed.Load() {
		return false
	}
	select {
	case k.Events <- event:
		return true
	default:
		return false
	}
}

func (k *Kernel) Close() {
	if k.closed.CompareAndSwap(false, true) {
		close(k.Events)
	}
}

// Execute is the single data-plane orchestration boundary. Provider adapters
// own protocol details; the kernel owns public-model resolution and selection.
func (k *Kernel) Execute(ctx context.Context, req NormalizedRequest, credential Credential, writer http.ResponseWriter) error {
	if req.Operation == "" {
		return ErrOperationRequired
	}
	if k.Operations == nil {
		return fmt.Errorf("operation catalog is not initialized")
	}
	externalRequirements := append([]normalize.FeatureRequirement(nil), req.Requirements...)
	preparedOperation, err := k.Operations.Prepare(req)
	if err != nil {
		return err
	}
	req.Requirements = mergeRequirements(externalRequirements, preparedOperation.Requirements)
	if k.Transforms.Active() {
		if err := k.Transforms.Apply(ctx, &req); err != nil {
			return err
		}
		preparedOperation, err = k.Operations.Prepare(req)
		if err != nil {
			return err
		}
		req.Requirements = mergeRequirements(externalRequirements, preparedOperation.Requirements)
	}
	started := time.Now()
	snapshot := k.Snapshots.Load()
	root, err := ResolvePublicNode(snapshot, req.Model)
	if err != nil {
		return err
	}
	return k.executeNode(ctx, snapshot, root, req, preparedOperation.ReplaySafety, credential, writer, started, map[string]bool{})
}

func mergeRequirements(required, derived []normalize.FeatureRequirement) []normalize.FeatureRequirement {
	result := make([]normalize.FeatureRequirement, 0, len(required)+len(derived))
	seen := make(map[string]struct{}, cap(result))
	for _, requirement := range append(append([]normalize.FeatureRequirement(nil), required...), derived...) {
		key := requirement.ID + "\x00" + string(requirement.Constraints)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, requirement)
	}
	return result
}

func (k *Kernel) adapterForRoute(route Route, request NormalizedRequest, requirements RequestRequirements) ProviderAdapter {
	operation := requirements.Operation
	operationBinding, ok := route.OperationBindings[operation]
	if !ok || requirements.OperationContractVersion == 0 || operationBinding.ContractVersion != requirements.OperationContractVersion {
		return nil
	}
	adapterIDs := operationBinding.AdapterIDs
	for _, adapterID := range adapterIDs {
		adapter := k.Adapters[adapterID]
		if adapter == nil {
			continue
		}
		compatibilityContext := CompatibilityContext{
			Request: request, Route: route, Operation: operation, Requirements: requirements,
			ActiveResponseTransform: k.ResponseTransforms.Active(),
		}
		compatibilityContext.Policy.RequiredFacets = RequiredRequestFacets(request)
		for _, event := range RequiredResponseEvents(request) {
			compatibilityContext.Policy.RequiredFacets = append(compatibilityContext.Policy.RequiredFacets, ResponseEventFacet(event))
		}
		plan := adapter.PlanCompatibility(compatibilityContext)
		policy := compatibilityContext.Policy
		policy.RequiredFacets = append(policy.RequiredFacets, FacetWireResponse)
		plan = ComposeCompatibilityPlan([][]FacetMapping{plan.Mappings}, policy)
		if !plan.Supported || (plan.Fidelity != FidelityNative && plan.Fidelity != FidelityTranslated) || len(plan.Losses) > 0 {
			continue
		}
		return adapter
	}
	return nil
}

func (k *Kernel) executeNode(ctx context.Context, snapshot Snapshot, node ModelNode, req NormalizedRequest, replaySafety operations.ReplaySafety, credential Credential, writer http.ResponseWriter, started time.Time, stack map[string]bool) error {
	if stack[node.ID] {
		return fmt.Errorf("model cycle at %q", node.ID)
	}
	stack[node.ID] = true
	defer delete(stack, node.ID)
	if req.Thinking.Mode == "" || req.Thinking.Mode == "inherit" {
		if node.Reasoning.Mode != "" && node.Reasoning.Mode != "inherit" {
			req.Thinking = node.Reasoning
		}
	}
	for _, member := range k.Scheduler.Plan(node) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if member.Kind == MemberModel {
			child, ok := snapshot.Nodes[member.ID]
			if !ok {
				return fmt.Errorf("unknown model node %q", member.ID)
			}
			err := k.executeNode(ctx, snapshot, child, req, replaySafety, credential, writer, started, stack)
			if err == nil {
				return nil
			} else if !errors.Is(err, ErrNoRoute) {
				return err
			}
			failure := StrategyFailure{NodeID: node.ID, Member: member, Class: ErrorRetryable, Err: err}
			if k.Scheduler.OnFailure(node, failure) == FailureStop {
				return err
			}
			continue
		}
		routes := k.Scheduler.RankRoutesForSession(resolveRouteMember(snapshot, member), time.Now(), requestClass(req), req.Session.ID)
		if node.Kind == ModelPhysical && !PhysicalSourceAllowed(node, member) {
			routes = nil
		}
		failureClass := ErrorRetryable
		var memberErr error = ErrNoRoute
		if preferred := req.Transport.PreferredConnectionID; preferred != "" {
			for i, candidate := range routes {
				if candidate.CredentialID == preferred && i > 0 {
					routes = append([]Route{candidate}, append(routes[:i:i], routes[i+1:]...)...)
					break
				}
			}
		}
		for _, candidate := range routes {
			requirements := CompileRequirements(req)
			operationBinding, ok := candidate.OperationBindings[requirements.Operation]
			if !ok || operationBinding.ContractVersion != requirements.OperationContractVersion {
				continue
			}
			candidate.ErrorClassifierID = operationBinding.ErrorClassifierID
			candidate.UsageSourceID = operationBinding.UsageSourceID
			candidate.UsageOptions = operationBinding.UsageOptions
			candidate.SessionStoreID = operationBinding.SessionStoreID
			if eligible, _ := Eligible(candidate, requirements); !eligible {
				continue
			}
			if eligible, _ := k.Features.Evaluate(candidate, requirements.Features); !eligible {
				continue
			}
			adapter := k.adapterForRoute(candidate, req, requirements)
			if adapter == nil {
				continue
			}
			if !k.Scheduler.Acquire(candidate, time.Now()) {
				failureClass = ErrorCooldown
				continue
			}
			selectedCredential := credential
			var err error
			if selectedCredential.Secret == "" && k.ResolveCredential != nil {
				selectedCredential, err = k.ResolveCredential(ctx, candidate)
				if err != nil {
					k.Scheduler.Release(candidate)
					failureClass, memberErr = ErrorAuth, err
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkFailure(candidate, ErrorAuth, err)
					}
					continue
				}
			}
			candidateRequest := req
			sessionKey := requestSessionKey(req)
			var sessionState SessionState
			if sessionKey != "" && candidate.SessionStoreID != "" {
				if sessionStore := k.SessionStores[candidate.SessionStoreID]; sessionStore != nil {
					if saved, found, loadErr := sessionStore.Load(ctx, candidate, sessionKey); loadErr == nil && found {
						sessionState = saved
						candidateRequest.Session.ProviderState = append([]byte(nil), saved.ProviderData...)
						if candidateRequest.Continuity.PreviousResponse == "" {
							candidateRequest.Continuity.PreviousResponse = saved.ResponseID
						}
					}
				}
			}
			refreshed := false
		retryUpstream:
			upstream, err := adapter.Prepare(ctx, candidateRequest, candidate, selectedCredential)
			if err != nil {
				k.Scheduler.Release(candidate)
				failureClass, memberErr = ErrorRetryable, err
				continue
			}
			response, err := adapter.Execute(ctx, upstream)
			if err != nil {
				k.Scheduler.Release(candidate)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failureClass, memberErr = kernelErrorClass(err), err
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: failureClass, Cause: CauseNetwork, Effect: EffectUnknown, Scope: ScopeRoute, Retry: RetryAfter, Confidence: 0.8, Evidence: []EvidenceSource{EvidenceInferred}, Message: err.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, kernelErrorClass(err), err)
				}
				return &ReplaySuppressedError{Operation: requirements.Operation, Safety: string(replaySafety), Effect: EffectUnknown, Cause: err}
			}
			if response.Status >= 400 {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
				class := adapter.ClassifyError(response.Status, body)
				outcome := classifyOutcome(adapter, response.Status, response.Headers, body, class)
				if classifier, ok := k.ErrorClassifiers[candidate.ErrorClassifierID]; ok {
					class = classifier.ClassifyError(response.Status, body)
					if rich, ok := classifier.(OutcomeClassifier); ok {
						outcome = rich.ClassifyOutcome(response.Status, response.Headers, body)
					}
				}
				outcome.Class = class
				if outcome.Effect == "" {
					outcome.Effect = inferUpstreamEffect(response.Status, outcome.Cause)
				}
				if (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) && !refreshed && k.RefreshCredential != nil && canReplayAfterDispatch(replaySafety, outcome.Effect) {
					refreshedCredential, refreshErr := k.RefreshCredential(ctx, candidate, selectedCredential)
					if refreshErr == nil && refreshedCredential.Secret != "" && refreshedCredential.Secret != selectedCredential.Secret {
						_ = response.Body.Close()
						selectedCredential = refreshedCredential
						refreshed = true
						goto retryUpstream
					}
				}
				k.Scheduler.Release(candidate)
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, outcome)
				}
				failureClass = class
				memberErr = fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body)))
				retryAfter := retryAfterDuration(response.Headers.Get("Retry-After"))
				if _, rich := k.Scheduler.gate.(OutcomeObserver); !rich {
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkFailureAfter(candidate, class, fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body))), retryAfter)
					}
				}
				_ = response.Body.Close()
				if !canReplayAfterDispatch(replaySafety, outcome.Effect) {
					return &ReplaySuppressedError{Operation: requirements.Operation, Safety: string(replaySafety), Effect: outcome.Effect, Cause: memberErr}
				}
				if class == ErrorTerminal {
					terminalErr := fmt.Errorf("upstream status %d: %s", response.Status, strings.TrimSpace(string(body)))
					failure := StrategyFailure{NodeID: node.ID, Member: member, Class: class, Err: terminalErr}
					if k.Scheduler.OnFailure(node, failure) == FailureStop {
						return terminalErr
					}
					memberErr = terminalErr
					continue
				}
				continue
			}
			defer response.Body.Close()
			var firstByteAt time.Time
			emitEvent := responseEventSink(writer)
			streamWriter := &responseCommitWriter{ResponseWriter: writer}
			var responseTransform func(context.Context, ResponseEvent) (ResponseEvent, error)
			if k.ResponseTransforms.Active() {
				responseTransform = k.transformResponse
			}
			attemptReleased := false
			releaseAttempt := func() {
				if !attemptReleased {
					k.Scheduler.Release(candidate)
					attemptReleased = true
				}
			}
			renderErr := adapter.RenderResponse(ctx, response, streamWriter, candidateRequest.SourceFormat, StreamHooks{Streaming: candidateRequest.Stream, Model: candidateRequest.Model, TransformResponse: responseTransform, OnError: func(streamErr error) {
				releaseAttempt()
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: streamErr.Error()})
				}
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorRetryable, Cause: CauseStreamFailure, Effect: EffectAccepted, Scope: ScopeRoute, Retry: RetryAfter, Confidence: 0.9, Evidence: []EvidenceSource{EvidenceInferred}, Message: streamErr.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, ErrorRetryable, streamErr)
				}
			}, OnEvent: func(event ResponseEvent) {
				if event.ResponseID != "" {
					sessionState.ResponseID = event.ResponseID
				}
				if emitEvent != nil {
					emitEvent(event)
				}
			}, OnSessionState: func(state SessionState) {
				if state.ResponseID != "" {
					sessionState.ResponseID = state.ResponseID
				}
				if len(state.ProviderData) > 0 {
					sessionState.ProviderData = append([]byte(nil), state.ProviderData...)
				}
				if !state.ExpiresAt.IsZero() {
					sessionState.ExpiresAt = state.ExpiresAt
				}
			}, OnFirstByte: func(at time.Time) {
				firstByteAt = at
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: at, Kind: EventResponseStarted})
				}
			}, OnComplete: func(event UsageEvent) {
				releaseAttempt()
				if sessionKey != "" && candidate.SessionStoreID != "" && (sessionState.ResponseID != "" || len(sessionState.ProviderData) > 0) {
					if sessionStore := k.SessionStores[candidate.SessionStoreID]; sessionStore != nil {
						_ = sessionStore.Save(ctx, candidate, sessionKey, sessionState)
					}
				}
				if source := k.UsageSources[candidate.UsageSourceID]; source != nil {
					if enriched, enrichErr := source.EnrichUsage(ctx, candidate, response.Headers, event); enrichErr == nil {
						event = enriched
					}
				}
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorTerminal, Cause: CauseSuccess, Effect: EffectAccepted, Scope: ScopeRoute, Retry: RetryNow, Confidence: 1, Evidence: []EvidenceSource{EvidenceSuccessBody}})
				}
				if _, rich := k.Scheduler.gate.(OutcomeObserver); !rich {
					if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkSuccess(candidate)
					}
				}
				if event.At.IsZero() {
					event.At = time.Now()
				}
				if event.Latency == 0 {
					event.Latency = time.Since(started)
				}
				if event.FirstByteAt.IsZero() {
					event.FirstByteAt = firstByteAt
				}
				if !event.FirstByteAt.IsZero() {
					event.TTFT = event.FirstByteAt.Sub(started)
				}
				if event.OutputTokens > 0 && event.Latency > 0 {
					event.OutputTokensPerSecond = float64(event.OutputTokens) / event.Latency.Seconds()
				}
				if event.LogicalModel == "" {
					event.LogicalModel = req.Model
				}
				if event.ProviderNodeID == "" {
					event.ProviderNodeID = candidate.NodeID
				}
				if event.ExternalModel == "" {
					event.ExternalModel = candidate.ExternalModel
				}
				if event.ConnectionID == "" {
					event.ConnectionID = candidate.CredentialID
				}
				if event.RequestClass == "" {
					event.RequestClass = requestClass(req)
				}
				if event.SessionID == "" {
					event.SessionID = req.Session.ID
				}
				if observer, ok := k.Scheduler.gate.(UsageObserver); ok {
					observer.ObserveUsage(candidate, event)
				}
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: event.At, Kind: EventResponseComplete, Usage: &event})
				}
				k.EmitUsage(event)
			}})
			if renderErr == nil {
				return nil
			}
			if !attemptReleased {
				releaseAttempt()
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorRetryable, Cause: CauseProtocol, Scope: ScopeRoute, Retry: RetryAfter, Confidence: 0.9, Evidence: []EvidenceSource{EvidenceInferred}, Message: renderErr.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, ErrorRetryable, renderErr)
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if streamWriter.committed {
				return renderErr
			}
			return &ReplaySuppressedError{Operation: requirements.Operation, Safety: string(replaySafety), Effect: EffectAccepted, Cause: renderErr}
		}
		failure := StrategyFailure{NodeID: node.ID, Member: member, Class: failureClass, Err: memberErr}
		if k.Scheduler.OnFailure(node, failure) == FailureStop {
			return ErrNoRoute
		}
	}
	return ErrNoRoute
}

func requestSessionKey(request NormalizedRequest) string {
	if request.Session.ID != "" {
		return request.Session.ID
	}
	return request.Session.Conversation
}

func PhysicalSourceAllowed(node ModelNode, source MemberRef) bool {
	if source.Fidelity == FidelityExact {
		return true
	}
	if source.Fidelity == FidelityAlias {
		return len(source.Evidence) > 0
	}
	if source.Fidelity == FidelityCompatible {
		return node.AllowCompatibleSources
	}
	if source.Fidelity == FidelityDynamic {
		return node.AllowDynamicSources
	}
	return false
}

type responseEventWriter interface{ ResponseEvent(ResponseEvent) }

func responseEventSink(writer http.ResponseWriter) func(ResponseEvent) {
	if sink, ok := writer.(responseEventWriter); ok {
		return sink.ResponseEvent
	}
	return nil
}

func requestClass(req NormalizedRequest) string {
	contextBucket := "empty"
	if len(req.Messages) > 32 {
		contextBucket = "large"
	} else if len(req.Messages) > 8 {
		contextBucket = "medium"
	} else if len(req.Messages) > 0 {
		contextBucket = "small"
	}
	return fmt.Sprintf("format=%s;stream=%t;vision=%t;audio=%t;video=%t;pdf=%t;tools=%t;thinking=%s;context=%s", req.SourceFormat, req.Stream, req.Modalities.Vision, req.Modalities.AudioInput, req.Modalities.VideoInput, req.Modalities.PDF, len(req.Tools) > 0, req.Thinking.Effort, contextBucket)
}

func retryAfterDuration(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if parsed, err := http.ParseTime(value); err == nil {
		if delay := time.Until(parsed); delay > 0 {
			return delay
		}
	}
	return 0
}

func classifyOutcome(adapter ProviderAdapter, status int, headers http.Header, body []byte, class ErrorClass) ClassifiedOutcome {
	if rich, ok := adapter.(OutcomeClassifier); ok {
		outcome := rich.ClassifyOutcome(status, headers, body)
		outcome.Class = class
		if outcome.Effect == "" {
			outcome.Effect = inferUpstreamEffect(status, outcome.Cause)
		}
		return outcome
	}
	outcome := ClassifiedOutcome{Class: class, Scope: ScopeRoute, Confidence: 0.5, Evidence: []EvidenceSource{EvidenceErrorBody}, StatusCode: status, Message: strings.TrimSpace(string(body))}
	switch {
	case status == 401 || status == 403:
		outcome.Cause, outcome.Retry = CauseAuth, RetryNever
	case status == 429:
		outcome.Cause, outcome.Retry = CauseRateLimited, RetryAfter
	case status >= 500:
		outcome.Cause, outcome.Retry = CauseCapacity, RetryAfter
	case status >= 400:
		outcome.Cause, outcome.Retry = CauseRequestInvalid, RetryNever
	default:
		outcome.Cause, outcome.Retry = CauseUnknown, RetryUnknown
	}
	outcome.Effect = inferUpstreamEffect(status, outcome.Cause)
	return outcome
}

func inferUpstreamEffect(status int, cause OutcomeCause) UpstreamEffect {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return EffectAccepted
	}
	if status < 400 || status >= 500 || status == http.StatusRequestTimeout || status == http.StatusConflict {
		return EffectUnknown
	}
	switch cause {
	case CauseRequestInvalid, CauseCapability, CauseModelNotFound, CauseAuth, CausePermission, CauseQuotaExhausted, CauseRateLimited:
		return EffectRejected
	default:
		return EffectUnknown
	}
}

func canReplayAfterDispatch(safety operations.ReplaySafety, effect UpstreamEffect) bool {
	return safety == operations.ReplayOnRejection && effect == EffectRejected
}

func kernelErrorClass(err error) ErrorClass {
	if err == nil {
		return ErrorRetryable
	}
	return ErrorCooldown
}
