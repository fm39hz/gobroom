package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
)

type Kernel struct {
	Snapshots          *SnapshotStore
	Scheduler          *Scheduler
	Adapters           map[string]ProviderAdapter
	ErrorClassifiers   map[extensions.Ref]ErrorClassifier
	UsageSources       map[extensions.Ref]UsageEnricher
	SessionStores      map[extensions.Ref]SessionStore
	Transforms         *RequestTransformRegistry
	ResponseTransforms *ResponseTransformRegistry
	Features           *FeatureRegistry
	Operations         *operations.Snapshot
	ArtifactStore      *artifacts.Store
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
	return &Kernel{Snapshots: store, Scheduler: NewScheduler(gate), Adapters: map[string]ProviderAdapter{}, ErrorClassifiers: map[extensions.Ref]ErrorClassifier{}, UsageSources: map[extensions.Ref]UsageEnricher{}, SessionStores: map[extensions.Ref]SessionStore{}, Features: NewFeatureRegistry(), Operations: operationSnapshot, Transforms: NewRequestTransformRegistry(), ResponseTransforms: NewResponseTransformRegistry(), Events: make(chan UsageEvent, buffer)}, nil
}

func daemonTransformScope() TransformScope {
	return TransformScope{Kind: TransformScopeDaemon}
}

func (k *Kernel) ValidateTransformBindings(bindings []TransformBinding) error {
	var requestBindings, responseBindings []TransformBinding
	for _, binding := range bindings {
		switch binding.TransformRef.Kind {
		case RequestTransformKind:
			requestBindings = append(requestBindings, binding)
		case ResponseTransformKind:
			responseBindings = append(responseBindings, binding)
		default:
			return fmt.Errorf("transform binding %q has unsupported kind %q", binding.ID, binding.TransformRef.Kind)
		}
	}
	if err := k.Transforms.ValidateBindings(requestBindings); err != nil {
		return err
	}
	return k.ResponseTransforms.ValidateBindings(responseBindings)
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
	if k.ArtifactStore != nil {
		ctx = artifacts.WithStore(ctx, k.ArtifactStore)
	}
	started := time.Now()
	if req.Operation == "" {
		return ErrOperationRequired
	}
	if k.Operations == nil {
		return fmt.Errorf("operation catalog is not initialized")
	}
	snapshot := k.Snapshots.Load()
	root, err := ResolvePublicNode(snapshot, req.Model)
	if err != nil {
		return err
	}
	externalRequirements := append([]normalize.FeatureRequirement(nil), req.Requirements...)
	preparedOperation, err := k.Operations.Prepare(req)
	if err != nil {
		return err
	}
	if deadline := preparedOperation.ResourceBounds.DeadlineMillis; deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(deadline)*time.Millisecond)
		defer cancel()
	}
	req.Requirements = mergeRequirements(externalRequirements, preparedOperation.Requirements)
	transformBindings := cloneTransformBindings(snapshot.TransformBindings)
	scopes := []TransformScope{daemonTransformScope()}
	if k.Transforms.Active(transformBindings, daemonTransformScope()) {
		if err := ctx.Err(); err != nil {
			return err
		}
		req = normalize.CloneRequest(req)
		if err := k.Transforms.ApplyScopes(ctx, &req, transformBindings, daemonTransformScope()); err != nil {
			return err
		}
		preparedOperation, err = k.Operations.Prepare(req)
		if err != nil {
			return err
		}
		req.Requirements = mergeRequirements(externalRequirements, preparedOperation.Requirements)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return k.executeNode(ctx, snapshot, root, req, preparedOperation.ReplaySafety, preparedOperation.ResourceBounds, externalRequirements, transformBindings, scopes, CompatibilityPolicy{LossCeiling: snapshot.LossCeiling}, credential, writer, started, map[string]bool{})
}

func mergeRequirements(required, derived []normalize.FeatureRequirement) []normalize.FeatureRequirement {
	result := make([]normalize.FeatureRequirement, 0, len(required)+len(derived))
	seen := make(map[string]struct{}, cap(result))
	for _, requirement := range append(append([]normalize.FeatureRequirement(nil), required...), derived...) {
		key := requirement.Ref.Key() + "\x00" + string(requirement.Constraints)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, requirement)
	}
	return result
}

func (k *Kernel) adapterForRoute(route Route, request NormalizedRequest, requirements RequestRequirements, transformBindings []TransformBinding, scopes []TransformScope, lossPolicy CompatibilityPolicy) (ProviderAdapter, CompatibilityPlan) {
	operation := requirements.Operation
	operationBinding, ok := route.OperationBindings[operation]
	if !ok || requirements.OperationContractVersion == 0 || operationBinding.ContractVersion != requirements.OperationContractVersion {
		return nil, CompatibilityPlan{Fidelity: FidelityUnsupported, Reason: "route has no matching operation binding"}
	}
	adapterIDs := operationBinding.AdapterIDs
	for _, adapterID := range adapterIDs {
		adapter := k.Adapters[adapterID]
		if adapter == nil {
			continue
		}
		requestTransforms := k.Transforms.Plan(transformBindings, scopes...)
		responseTransforms := k.ResponseTransforms.Plan(transformBindings, scopes...)
		operationRef := extensions.Ref{Kind: "operation", ID: string(operation), ContractVersion: operationBinding.ContractVersion}
		compatibilityContext := CompatibilityContext{
			Request: request, Route: route, Operation: operation, Requirements: requirements,
			ActiveResponseTransform: len(responseTransforms) > 0,
			RequestTransformSteps:   requestTransforms,
			ResponseTransformSteps:  responseTransforms,
			ArtifactTransfers:       PlanArtifactTransfers(request, operationRef, route.DefinitionRef),
		}
		compatibilityContext.Policy.RequiredFacets = RequiredRequestFacets(request)
		compatibilityContext.Policy.AllowedLosses = append([]string(nil), lossPolicy.AllowedLosses...)
		compatibilityContext.Policy.DeniedLosses = append([]string(nil), lossPolicy.DeniedLosses...)
		compatibilityContext.Policy.LossSources = cloneLossSources(lossPolicy.LossSources)
		compatibilityContext.Policy.LossCeiling = cloneLossCeiling(lossPolicy.LossCeiling)
		for _, event := range RequiredResponseEvents(request) {
			compatibilityContext.Policy.RequiredFacets = append(compatibilityContext.Policy.RequiredFacets, ResponseEventFacet(event))
		}
		plan := adapter.PlanCompatibility(compatibilityContext)
		policy := compatibilityContext.Policy
		policy.RequiredFacets = append(policy.RequiredFacets, FacetWireResponse)
		plan = ComposeCompatibilityPlan([][]FacetMapping{plan.Mappings}, policy)
		plan = cloneCompatibilityPlanContext(plan, compatibilityContext)
		if !plan.Supported || (plan.Fidelity != FidelityNative && plan.Fidelity != FidelityTranslated && plan.Fidelity != FidelityLossy) {
			continue
		}
		return adapter, plan
	}
	return nil, CompatibilityPlan{Fidelity: FidelityUnsupported, Reason: "no adapter satisfies the effective compatibility policy"}
}

func cloneLossSources(sources map[string][]string) map[string][]string {
	if sources == nil {
		return nil
	}
	clone := make(map[string][]string, len(sources))
	for id, nodes := range sources {
		clone[id] = append([]string(nil), nodes...)
	}
	return clone
}

func (k *Kernel) executeNode(ctx context.Context, snapshot Snapshot, node ModelNode, req NormalizedRequest, replaySafety operations.ReplaySafety, resourceBounds extensions.ResourceBounds, externalRequirements []normalize.FeatureRequirement, transformBindings []TransformBinding, scopes []TransformScope, lossPolicy CompatibilityPolicy, credential Credential, writer http.ResponseWriter, started time.Time, stack map[string]bool) error {
	if stack[node.ID] {
		return fmt.Errorf("model cycle at %q", node.ID)
	}
	stack[node.ID] = true
	defer delete(stack, node.ID)
	lossPolicy = AddLossPolicy(lossPolicy, node.ID, node.LossPolicy)
	if req.Thinking.Mode == "" || req.Thinking.Mode == "inherit" {
		if node.Reasoning.Mode != "" && node.Reasoning.Mode != "inherit" {
			req.Thinking = node.Reasoning
		}
	}
	modelScope := TransformScope{Kind: TransformScopeModel, ID: node.ID}
	scopes = append(append([]TransformScope(nil), scopes...), modelScope)
	if k.Transforms.Active(transformBindings, modelScope) {
		req = normalize.CloneRequest(req)
		if err := k.Transforms.ApplyScopes(ctx, &req, transformBindings, modelScope); err != nil {
			return err
		}
		prepared, err := k.Operations.Prepare(req)
		if err != nil {
			return err
		}
		req.Requirements = mergeRequirements(externalRequirements, prepared.Requirements)
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
			err := k.executeNode(ctx, snapshot, child, req, replaySafety, resourceBounds, externalRequirements, transformBindings, scopes, lossPolicy, credential, writer, started, stack)
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
			candidateRequest := req
			attemptScopes := []TransformScope{{Kind: TransformScopeRoute, ID: candidate.ID}}
			if candidate.NodeID != "" {
				attemptScopes = append(attemptScopes, TransformScope{Kind: TransformScopeProvider, ID: candidate.NodeID})
			}
			if candidate.CredentialID != "" {
				attemptScopes = append(attemptScopes, TransformScope{Kind: TransformScopeConnection, ID: candidate.CredentialID})
			}
			candidateScopes := append(append([]TransformScope(nil), scopes...), attemptScopes...)
			if k.Transforms.Active(transformBindings, attemptScopes...) {
				candidateRequest = normalize.CloneRequest(req)
				if err := k.Transforms.ApplyScopes(ctx, &candidateRequest, transformBindings, attemptScopes...); err != nil {
					return err
				}
				prepared, err := k.Operations.Prepare(candidateRequest)
				if err != nil {
					return err
				}
				candidateRequest.Requirements = mergeRequirements(externalRequirements, prepared.Requirements)
			}
			requirements := CompileRequirements(candidateRequest)
			operationBinding, ok := candidate.OperationBindings[requirements.Operation]
			if !ok || operationBinding.ContractVersion != requirements.OperationContractVersion {
				continue
			}
			candidate.ErrorClassifierRef = operationBinding.ErrorClassifierRef
			candidate.UsageSourceRef = operationBinding.UsageSourceRef
			candidate.UsageOptions = operationBinding.UsageOptions
			candidate.SessionStoreRef = operationBinding.SessionStoreRef
			operationRef := extensions.Ref{Kind: "operation", ID: string(requirements.Operation), ContractVersion: requirements.OperationContractVersion}
			receiver := extensions.ArtifactOwner{
				Domain: "provider", IssuerRef: &candidate.DefinitionRef,
				ProviderDefinitionID: candidate.DefinitionID, ConnectionID: candidate.CredentialID,
				ModelIdentity: candidate.ExternalModel, ClientContract: string(candidateRequest.SourceFormat),
				SessionID: requestSessionKey(candidateRequest),
			}
			if err := k.Operations.ValidateArtifactsForConsumer(operationRef, candidateRequest.Artifacts, receiver, candidate.DefinitionRef); err != nil {
				failureClass, memberErr = ErrorCapability, err
				continue
			}
			if k.ArtifactStore == nil && hasBodyArtifacts(candidateRequest.Artifacts) {
				failureClass, memberErr = ErrorCapability, fmt.Errorf("artifact body store is unavailable")
				continue
			}
			if eligible, _ := Eligible(candidate, requirements); !eligible {
				continue
			}
			if eligible, _ := k.Features.Evaluate(candidate, requirements.Features); !eligible {
				continue
			}
			adapter, compatibilityPlan := k.adapterForRoute(candidate, candidateRequest, requirements, transformBindings, candidateScopes, lossPolicy)
			if adapter == nil {
				continue
			}
			var resultProjector operations.ResultProjector
			if definition, exists := k.Operations.Resolve(operationRef); exists && definition.ResultSchemaRef != nil {
				projector, projectorErr := k.Operations.NewResultProjector(operationRef)
				if projectorErr != nil {
					failureClass, memberErr = ErrorCapability, projectorErr
					continue
				}
				resultProjector = projector
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
			sessionKey := requestSessionKey(req)
			var sessionState SessionState
			if sessionKey != "" && candidate.SessionStoreRef.ID != "" {
				if sessionStore := k.SessionStores[candidate.SessionStoreRef]; sessionStore != nil {
					if saved, found, loadErr := sessionStore.Load(ctx, candidate, sessionKey); loadErr == nil && found {
						sessionState = saved
						candidateRequest.Session.ProviderState = append([]byte(nil), saved.ProviderData...)
						if candidateRequest.Continuity.PreviousResponse == "" {
							candidateRequest.Continuity.PreviousResponse = saved.ResponseID
						}
					}
				}
			}
			attemptCtx := ctx
			if k.ArtifactStore != nil {
				attemptCtx = artifacts.WithAccess(ctx, artifacts.Access{Store: k.ArtifactStore, Catalog: k.Operations.ExtensionCatalog(), Receiver: receiver, Recipient: candidate.DefinitionRef})
			}
			refreshed := false
		retryUpstream:
			upstream, err := adapter.Prepare(attemptCtx, candidateRequest, candidate, selectedCredential)
			if err != nil {
				k.Scheduler.Release(candidate)
				failureClass, memberErr = ErrorRetryable, err
				continue
			}
			response, err := adapter.Execute(attemptCtx, upstream)
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
				if classifier, ok := k.ErrorClassifiers[candidate.ErrorClassifierRef]; ok {
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
			streamWriter := &responseCommitWriter{ResponseWriter: writer, maxBytes: resourceBounds.MaxOutputBytes}
			var validateEvent func(context.Context, ResponseEvent) error
			if definition, exists := k.Operations.Resolve(operationRef); exists && (len(definition.EventSchemaRefs) > 0 || resultProjector != nil) {
				var sequence uint64
				resultFinalized := false
				validateEvent = func(_ context.Context, event ResponseEvent) error {
					sequence++
					payload, err := operationEventPayload(event, sequence)
					if err != nil {
						return err
					}
					if err := k.Operations.ValidateEvent(operationRef, payload); err != nil {
						return err
					}
					if resultProjector == nil {
						return nil
					}
					if resultFinalized {
						return fmt.Errorf("operation %q emitted events after its terminal result", operationRef.Key())
					}
					if err := resultProjector.ConsumeEvent(payload); err != nil {
						return fmt.Errorf("project operation %q result: %w", operationRef.Key(), err)
					}
					if event.Kind == EventResponseComplete {
						result, err := resultProjector.Finalize()
						if err != nil {
							return fmt.Errorf("finalize operation %q result: %w", operationRef.Key(), err)
						}
						if err := k.Operations.ValidateResult(operationRef, result); err != nil {
							return err
						}
						resultFinalized = true
					}
					return nil
				}
			}
			var responseTransform func(context.Context, ResponseEvent) (ResponseEvent, error)
			responseTransformScopes := append([]TransformScope(nil), candidateScopes...)
			if k.ResponseTransforms.Active(transformBindings, responseTransformScopes...) {
				responseTransform = func(transformCtx context.Context, event ResponseEvent) (ResponseEvent, error) {
					return k.ResponseTransforms.ApplyScopes(transformCtx, event, transformBindings, responseTransformScopes...)
				}
			}
			attemptReleased := false
			releaseAttempt := func() {
				if !attemptReleased {
					k.Scheduler.Release(candidate)
					attemptReleased = true
				}
			}
			renderErr := adapter.RenderResponse(attemptCtx, response, streamWriter, candidateRequest.SourceFormat, StreamHooks{Streaming: candidateRequest.Stream, Model: candidateRequest.Model, MaxEventBytes: resourceBounds.MaxBufferedBytes, MaxOutputBytes: resourceBounds.MaxOutputBytes, TransformResponse: responseTransform, OnError: func(streamErr error) {
				releaseAttempt()
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: streamErr.Error()})
				}
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorRetryable, Cause: CauseStreamFailure, Effect: EffectAccepted, Scope: ScopeRoute, Retry: RetryAfter, Confidence: 0.9, Evidence: []EvidenceSource{EvidenceInferred}, Message: streamErr.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, ErrorRetryable, streamErr)
				}
			}, ValidateEvent: validateEvent, OnEvent: func(event ResponseEvent) {
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
				event.CompatibilityFidelity = compatibilityPlan.Fidelity
				event.CompatibilityLosses = cloneLossRecords(compatibilityPlan.Losses)
				compatibilitySummary := SummarizeCompatibilityPlan(compatibilityPlan)
				event.CompatibilityPlan = &compatibilitySummary
				releaseAttempt()
				if sessionKey != "" && candidate.SessionStoreRef.ID != "" && (sessionState.ResponseID != "" || len(sessionState.ProviderData) > 0) {
					if sessionStore := k.SessionStores[candidate.SessionStoreRef]; sessionStore != nil {
						_ = sessionStore.Save(ctx, candidate, sessionKey, sessionState)
					}
				}
				if source := k.UsageSources[candidate.UsageSourceRef]; source != nil {
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

func hasBodyArtifacts(artifacts []extensions.ArtifactRef) bool {
	for _, artifact := range artifacts {
		if artifact.Body != nil {
			return true
		}
	}
	return false
}

type operationEventEnvelope struct {
	Kind     string         `json:"kind"`
	Sequence uint64         `json:"sequence"`
	ItemID   string         `json:"itemId,omitempty"`
	Content  map[string]any `json:"content"`
}

func operationEventPayload(event ResponseEvent, sequence uint64) (json.RawMessage, error) {
	content := make(map[string]any)
	for key, value := range map[string]string{
		"responseId": event.ResponseID, "contentType": event.ContentType, "blockType": event.BlockType,
		"stopReason": event.StopReason, "text": event.Text, "toolCallId": event.ToolCallID,
		"toolName": event.ToolName, "toolArguments": event.ToolArguments, "signature": event.Signature, "error": event.Error,
		"wireFormat": string(event.WireFormat),
	} {
		if value != "" {
			content[key] = value
		}
	}
	if event.Index != 0 {
		content["index"] = event.Index
	}
	if event.Usage != nil {
		content["usage"] = map[string]any{
			"inputTokens": event.Usage.InputTokens, "outputTokens": event.Usage.OutputTokens,
			"estimatedCost": event.Usage.EstimatedCost, "status": event.Usage.Status,
		}
	}
	payload, err := json.Marshal(operationEventEnvelope{Kind: string(event.Kind), Sequence: sequence, ItemID: event.ItemID, Content: content})
	if err != nil {
		return nil, fmt.Errorf("encode operation response event: %w", err)
	}
	return payload, nil
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
