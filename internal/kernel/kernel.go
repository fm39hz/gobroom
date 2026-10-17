package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
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
	operationRef := extensions.Ref{Kind: "operation", ID: string(req.Operation), ContractVersion: req.OperationContractVersion}
	if definition, exists := k.Operations.Resolve(operationRef); exists && definition.ResourceBounds.DeadlineMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, time.Duration(definition.ResourceBounds.DeadlineMillis)*time.Millisecond, ErrOperationDeadline)
		defer cancel()
	}
	externalRequirements := append([]normalize.FeatureRequirement(nil), req.Requirements...)
	preparedOperation, err := k.prepareOperation(ctx, &req)
	if err != nil {
		return err
	}
	req.Requirements = mergeRequirements(externalRequirements, preparedOperation.Requirements)
	transformBindings := cloneTransformBindings(snapshot.TransformBindings)
	scopes := []TransformScope{daemonTransformScope()}
	var transformFailures []TransformFailure
	if k.Transforms.Active(transformBindings, daemonTransformScope()) {
		if err := ctx.Err(); err != nil {
			return err
		}
		req = normalize.CloneRequest(req)
		report, err := k.Transforms.ApplyScopesWithReport(ctx, &req, transformBindings, daemonTransformScope())
		if err != nil {
			return err
		}
		transformFailures = append(transformFailures, report.Failures...)
		preparedOperation, err = k.prepareOperation(ctx, &req)
		if err != nil {
			return err
		}
		req.Requirements = mergeRequirements(externalRequirements, preparedOperation.Requirements)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return k.executeNode(ctx, snapshot, root, req, preparedOperation.ReplaySafety, preparedOperation.ResourceBounds, externalRequirements, transformBindings, scopes, transformFailures, CompatibilityPolicy{LossCeiling: snapshot.LossCeiling}, credential, writer, started, map[string]bool{})
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

func (k *Kernel) prepareOperation(ctx context.Context, request *NormalizedRequest) (operations.Prepared, error) {
	if k.Operations == nil || request == nil {
		return operations.Prepared{}, fmt.Errorf("operation catalog and request are required")
	}
	var openBody operations.ArtifactBodyOpener
	if k.ArtifactStore != nil {
		openBody = func(openCtx context.Context, artifact extensions.ArtifactRef, recipient extensions.Ref) (io.ReadCloser, error) {
			receiver := extensions.ArtifactOwner{Domain: "harness"}
			return k.ArtifactStore.OpenArtifact(openCtx, k.Operations.ExtensionCatalog(), artifact, receiver, recipient)
		}
	}
	prepared, err := k.Operations.PrepareWithArtifactBodies(ctx, *request, openBody)
	if err != nil {
		return operations.Prepared{}, err
	}
	request.ArtifactFacts = cloneOperationArtifactFacts(prepared.ArtifactFacts)
	return prepared, nil
}

func cloneOperationArtifactFacts(facts []normalize.ArtifactFacts) []normalize.ArtifactFacts {
	cloned := make([]normalize.ArtifactFacts, len(facts))
	for index, fact := range facts {
		fact.Value = append(json.RawMessage(nil), fact.Value...)
		cloned[index] = fact
	}
	return cloned
}

func (k *Kernel) adapterForRoute(route Route, request NormalizedRequest, requirements RequestRequirements, transformBindings []TransformBinding, scopes []TransformScope, transformFailures []TransformFailure, lossPolicy CompatibilityPolicy) (ProviderAdapter, CompatibilityPlan) {
	request.ArtifactFacts = nil
	operation := requirements.Operation
	operationBinding, ok := route.OperationBindings[operation]
	if !ok || requirements.OperationContractVersion == 0 || operationBinding.ContractVersion != requirements.OperationContractVersion {
		return nil, CompatibilityPlan{Fidelity: FidelityUnsupported, Reason: "route has no matching operation binding"}
	}
	operationRef := extensions.Ref{Kind: "operation", ID: string(operation), ContractVersion: operationBinding.ContractVersion}
	artifactOutputs, _ := k.Operations.ArtifactOutputs(operationRef)
	if len(artifactOutputs) > 0 && k.ArtifactStore == nil {
		return nil, CompatibilityPlan{Fidelity: FidelityUnsupported, Reason: "response artifact body store is unavailable"}
	}
	adapterIDs := operationBinding.AdapterIDs
	for _, adapterID := range adapterIDs {
		adapter := k.Adapters[adapterID]
		if adapter == nil {
			continue
		}
		requestTransforms := k.Transforms.Plan(transformBindings, scopes...)
		responseTransforms := k.ResponseTransforms.Plan(transformBindings, scopes...)
		compatibilityContext := CompatibilityContext{
			Request: request, Route: route, Operation: operation, Requirements: requirements,
			ActiveResponseTransform: len(responseTransforms) > 0,
			RequestTransformSteps:   requestTransforms,
			ResponseTransformSteps:  responseTransforms,
			ArtifactTransfers:       PlanArtifactTransfers(request, operationRef, route.DefinitionRef),
			TransformFailures:       append([]TransformFailure(nil), transformFailures...),
		}
		if len(artifactOutputs) > 0 {
			compatibilityContext.ArtifactOutputs = append([]extensions.ArtifactOutput(nil), artifactOutputs...)
			compatibilityContext.ArtifactOutputRequired = true
		}
		compatibilityContext.Policy.RequiredFacets = RequiredRequestFacets(request)
		compatibilityContext.Policy.AllowedLosses = append([]string(nil), lossPolicy.AllowedLosses...)
		compatibilityContext.Policy.DeniedLosses = append([]string(nil), lossPolicy.DeniedLosses...)
		compatibilityContext.Policy.LossSources = cloneLossSources(lossPolicy.LossSources)
		compatibilityContext.Policy.LossCeiling = cloneLossCeiling(lossPolicy.LossCeiling)
		for _, event := range RequiredResponseEvents(request) {
			compatibilityContext.Policy.RequiredFacets = append(compatibilityContext.Policy.RequiredFacets, ResponseEventFacet(event))
		}
		if len(artifactOutputs) > 0 {
			compatibilityContext.Policy.RequiredFacets = append(compatibilityContext.Policy.RequiredFacets, ResponseEventFacet(EventArtifact))
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

func (k *Kernel) executeNode(ctx context.Context, snapshot Snapshot, node ModelNode, req NormalizedRequest, replaySafety operations.ReplaySafety, resourceBounds extensions.ResourceBounds, externalRequirements []normalize.FeatureRequirement, transformBindings []TransformBinding, scopes []TransformScope, transformFailures []TransformFailure, lossPolicy CompatibilityPolicy, credential Credential, writer http.ResponseWriter, started time.Time, stack map[string]bool) error {
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
		report, err := k.Transforms.ApplyScopesWithReport(ctx, &req, transformBindings, modelScope)
		if err != nil {
			return err
		}
		transformFailures = append(append([]TransformFailure(nil), transformFailures...), report.Failures...)
		prepared, err := k.prepareOperation(ctx, &req)
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
			err := k.executeNode(ctx, snapshot, child, req, replaySafety, resourceBounds, externalRequirements, transformBindings, scopes, append([]TransformFailure(nil), transformFailures...), lossPolicy, credential, writer, started, stack)
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
		candidates := resolveRouteMember(snapshot, member)
		for index := range candidates {
			candidates[index].SessionOperationRef = extensions.Ref{Kind: "operation", ID: string(req.Operation), ContractVersion: req.OperationContractVersion}
		}
		routes := k.Scheduler.RankRoutesForSession(candidates, time.Now(), requestClass(req), req.Session.ID)
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
			candidateTransformFailures := append([]TransformFailure(nil), transformFailures...)
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
				report, err := k.Transforms.ApplyScopesWithReport(ctx, &candidateRequest, transformBindings, attemptScopes...)
				if err != nil {
					return err
				}
				candidateTransformFailures = append(candidateTransformFailures, report.Failures...)
				prepared, err := k.prepareOperation(ctx, &candidateRequest)
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
			candidate.SessionOperationRef = extensions.Ref{Kind: "operation", ID: string(requirements.Operation), ContractVersion: requirements.OperationContractVersion}
			sessionKey := requestSessionKey(req)
			var sessionState SessionState
			if sessionKey != "" && candidate.SessionStoreRef.ID != "" {
				sessionStore := k.SessionStores[candidate.SessionStoreRef]
				if sessionStore == nil {
					return ErrSessionStateUnavailable
				}
				saved, found, loadErr := sessionStore.Load(ctx, candidate, sessionKey)
				if loadErr != nil {
					return ErrSessionStateUnavailable
				}
				if found {
					sessionState = saved
					candidateRequest.Session.ProviderState = append([]byte(nil), saved.ProviderData...)
					if candidateRequest.Continuity.PreviousResponse == "" {
						candidateRequest.Continuity.PreviousResponse = saved.ResponseID
					}
				}
			}
			if requestHasToolSignature(candidateRequest.Messages) {
				candidateRequest = normalize.CloneRequest(candidateRequest)
				attestToolCallSignatures(&candidateRequest, sessionState.Signatures, candidate.DefinitionRef)
			}
			operationRef := candidate.SessionOperationRef
			artifactOutputs, _ := k.Operations.ArtifactOutputs(operationRef)
			if len(artifactOutputs) > 0 && k.ArtifactStore == nil {
				failureClass, memberErr = ErrorCapability, fmt.Errorf("response artifact body store is unavailable")
				continue
			}
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
			adapter, compatibilityPlan := k.adapterForRoute(candidate, candidateRequest, requirements, transformBindings, candidateScopes, candidateTransformFailures, lossPolicy)
			if adapter == nil {
				continue
			}
			idempotency := operationBinding.Idempotency[adapter.ID()]
			idempotencyKey := ""
			if replaySafety == operations.ReplayWithKey && candidateRequest.Transport.IdempotencyKey != "" && idempotency.Validate() == nil {
				idempotencyKey = deriveIssuerIdempotencyKey(candidateRequest.Transport.IdempotencyKey, candidate, operationRef)
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
			if err := selectedCredential.ValidateRuntimeContext(); err != nil {
				k.Scheduler.Release(candidate)
				failureClass, memberErr = ErrorAuth, err
				if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, ErrorAuth, err)
				}
				continue
			}
			attemptCtx := ctx
			if k.ArtifactStore != nil {
				attemptCtx = artifacts.WithAccess(ctx, artifacts.Access{Store: k.ArtifactStore, Catalog: k.Operations.ExtensionCatalog(), Receiver: receiver, Recipient: candidate.DefinitionRef})
			}
			var responseScope *artifacts.ResponseScope
			var responseArtifactAccess *artifacts.Access
			if len(artifactOutputs) > 0 {
				clientOwner := extensions.ArtifactOwner{Domain: "harness", ClientContract: string(candidateRequest.SourceFormat), SessionID: requestSessionKey(candidateRequest)}
				responseScope, err = artifacts.NewResponseScope(k.ArtifactStore, k.Operations.ExtensionCatalog(), receiver, clientOwner, operationRef, artifactOutputs, resourceBounds.MaxOutputBytes)
				if err != nil {
					k.Scheduler.Release(candidate)
					failureClass, memberErr = ErrorCapability, err
					continue
				}
				attemptCtx = artifacts.WithResponseScope(attemptCtx, responseScope)
				access := responseScope.Access()
				responseArtifactAccess = &access
			}
			refreshed := false
			if idempotencyKey != "" {
				attemptCtx = withPreparedIdempotency(attemptCtx, idempotency.Header, idempotencyKey)
			}
			idempotencyRetried := false
			var firstDispatch time.Time
			var replay PreparedRequestReplay
			var preparedMethod, preparedURL, preparedMediaType string
		retryUpstream:
			providerRequest := candidateRequest
			providerRequest.ArtifactFacts = nil
			var upstream UpstreamRequest
			var prepareErr error
			if idempotencyRetried {
				upstream, prepareErr = replay(attemptCtx, selectedCredential)
			} else if planned, ok := adapter.(CompatibilityPlanPreparer); ok {
				upstream, prepareErr = planned.PrepareWithCompatibility(attemptCtx, providerRequest, candidate, selectedCredential, compatibilityPlan)
			} else {
				upstream, prepareErr = adapter.Prepare(attemptCtx, providerRequest, candidate, selectedCredential)
			}
			if prepareErr != nil {
				if body, ok := upstream.Body.(io.Closer); ok {
					_ = body.Close()
				}
				k.Scheduler.Release(candidate)
				if idempotencyRetried {
					return &ReplaySuppressedError{Operation: requirements.Operation, Safety: string(replaySafety), Effect: EffectUnknown, Cause: prepareErr}
				}
				failureClass, memberErr = ErrorRetryable, prepareErr
				continue
			}
			if idempotencyKey != "" {
				if !idempotencyRetried {
					replay = upstream.Replay
					preparedMethod, preparedURL, preparedMediaType = upstream.Method, upstream.URL, upstream.Headers.Get("Content-Type")
				}
				if upstream.Headers == nil {
					upstream.Headers = make(http.Header)
				}
				upstream.Headers.Set(idempotency.Header, idempotencyKey)
			}
			dispatchErr := attemptCtx.Err()
			if dispatchErr == nil && idempotencyRetried && (upstream.Method != preparedMethod || upstream.URL != preparedURL || upstream.Headers.Get("Content-Type") != preparedMediaType) {
				dispatchErr = &ReplaySuppressedError{Operation: requirements.Operation, Safety: string(replaySafety), Effect: EffectUnknown, Cause: fmt.Errorf("prepared replay changed its action or media framing")}
			}
			if dispatchErr == nil && idempotencyRetried && !idempotency.permitsReplay(firstDispatch, time.Now()) {
				dispatchErr = &ReplaySuppressedError{Operation: requirements.Operation, Safety: string(replaySafety), Effect: EffectUnknown, Cause: fmt.Errorf("provider idempotency retention window expired before replay dispatch")}
			}
			if dispatchErr != nil {
				if body, ok := upstream.Body.(io.Closer); ok {
					_ = body.Close()
				}
				k.Scheduler.Release(candidate)
				return dispatchErr
			}
			if idempotencyKey != "" && firstDispatch.IsZero() {
				firstDispatch = time.Now()
			}
			response, err := adapter.Execute(attemptCtx, upstream)
			if err != nil {
				if ctx.Err() != nil {
					if errors.Is(context.Cause(ctx), ErrOperationDeadline) {
						if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
							observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorCooldown, Cause: CauseTimeout, Effect: EffectUnknown, Scope: ScopeOperation, Retry: RetryAfter, Confidence: 1, Evidence: []EvidenceSource{EvidenceInferred}, Message: ctx.Err().Error()})
						}
					}
					k.Scheduler.Release(candidate)
					return ctx.Err()
				}
				if idempotencyKey != "" && replay != nil && !idempotencyRetried && idempotency.permitsReplay(firstDispatch, time.Now()) {
					if body, ok := upstream.Body.(io.Closer); ok {
						_ = body.Close()
					}
					idempotencyRetried = true
					goto retryUpstream
				}
				failureClass, memberErr = kernelErrorClass(err), err
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: failureClass, Cause: CauseNetwork, Effect: EffectUnknown, Scope: ScopeOperation, Retry: RetryAfter, Confidence: 0.8, Evidence: []EvidenceSource{EvidenceInferred}, Message: err.Error()})
				} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
					feedback.MarkFailure(candidate, kernelErrorClass(err), err)
				}
				k.Scheduler.Release(candidate)
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
				if idempotencyKey != "" && replay != nil && !idempotencyRetried && idempotency.permitsReplay(firstDispatch, time.Now()) && outcome.Effect == EffectUnknown && outcome.Retry == RetryAfter {
					if body, ok := upstream.Body.(io.Closer); ok {
						_ = body.Close()
					}
					_ = response.Body.Close()
					idempotencyRetried = true
					goto retryUpstream
				}
				canRefresh := canReplayAfterDispatch(replaySafety, outcome.Effect) || (replaySafety == operations.ReplayWithKey && idempotencyKey != "" && replay != nil && !idempotencyRetried && idempotency.permitsReplay(firstDispatch, time.Now()))
				if (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden) && !refreshed && k.RefreshCredential != nil && canRefresh {
					refreshedCredential, refreshErr := k.RefreshCredential(ctx, candidate, selectedCredential)
					if refreshErr == nil && refreshedCredential.ConnectionID == selectedCredential.ConnectionID && refreshedCredential.Type == selectedCredential.Type && refreshedCredential.ValidateRuntimeContext() == nil && !reflect.DeepEqual(refreshedCredential, selectedCredential) {
						if body, ok := upstream.Body.(io.Closer); ok {
							_ = body.Close()
						}
						_ = response.Body.Close()
						selectedCredential = refreshedCredential
						refreshed = true
						if replaySafety == operations.ReplayWithKey && idempotencyKey != "" {
							idempotencyRetried = true
						}
						goto retryUpstream
					}
				}
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
				k.Scheduler.Release(candidate)
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
			if response.Body != nil {
				response.Body = &responseBodyLease{ReadCloser: response.Body}
				defer response.Body.Close()
			}
			var firstByteAt time.Time
			emitEvent := responseEventSink(writer)
			streamWriter := &responseCommitWriter{ResponseWriter: writer, maxBytes: resourceBounds.MaxOutputBytes, ctx: ctx}
			artifactOutputCounts := make(map[string]int, len(artifactOutputs))
			var sequence uint64
			resultFinalized := false
			signatureContextDigest := ""
			signatureContextReady := false
			validateEvent := func(_ context.Context, event ResponseEvent) error {
				sequence++
				if event.Kind == EventThinkingSignature || event.Signature != "" || event.SignatureIssuer.ID != "" {
					if event.SignatureIssuer.ID == "" {
						return ErrSignatureIssuerMissing
					}
					if candidate.DefinitionRef.Kind != "provider-definition" || candidate.DefinitionRef.Validate() != nil {
						return ErrSignatureIssuerMissing
					}
					if event.SignatureIssuer != candidate.DefinitionRef {
						return ErrSignatureIssuerMismatch
					}
					if event.Signature != "" && event.Kind == EventToolCallDelta && event.ToolCallID != "" && event.ToolName != "" && sessionKey != "" && candidate.SessionStoreRef.ID != "" {
						if !signatureContextReady {
							digest, digestErr := SignatureContextDigest(candidateRequest, len(candidateRequest.Messages))
							if digestErr != nil {
								return fmt.Errorf("could not bind provider signature context")
							}
							signatureContextDigest = digest
							signatureContextReady = true
						}
						call := normalize.ToolCall{ID: event.ToolCallID, Type: "function", Name: event.ToolName, Arguments: json.RawMessage(event.ToolArguments)}
						proofs, proofErr := appendSignatureProof(sessionState.Signatures, event.Signature, event.SignatureIssuer, signatureContextDigest, call)
						if proofErr != nil {
							return proofErr
						}
						sessionState.Signatures = proofs
					}
				}
				if len(event.Artifacts) > 0 {
					if len(artifactOutputs) == 0 || event.Kind != EventArtifact {
						return fmt.Errorf("operation %q emitted artifacts outside its declared artifact event", operationRef.Key())
					}
					for _, artifact := range event.Artifacts {
						if !artifact.Owner.Equal(receiver) {
							return fmt.Errorf("response artifact %q is not owned by the selected provider route", artifact.TypeRef.Key())
						}
					}
					clientOwner := extensions.ArtifactOwner{Domain: "harness", ClientContract: string(candidateRequest.SourceFormat), SessionID: requestSessionKey(candidateRequest)}
					if err := k.Operations.ValidateArtifactOutputs(operationRef, event.Artifacts, clientOwner, operationRef, artifactOutputCounts); err != nil {
						return err
					}
				} else if event.Kind == EventArtifact {
					return fmt.Errorf("operation %q emitted an empty artifact event", operationRef.Key())
				}
				if event.Kind == EventResponseComplete {
					if err := k.Operations.ValidateArtifactOutputCounts(operationRef, artifactOutputCounts); err != nil {
						return err
					}
				}
				payload, err := operationEventPayload(event, sequence)
				if err != nil {
					return err
				}
				if definition, exists := k.Operations.Resolve(operationRef); exists && len(definition.EventSchemaRefs) > 0 {
					if err := k.Operations.ValidateEvent(operationRef, payload); err != nil {
						return err
					}
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
			var responseTransform func(context.Context, ResponseEvent) (ResponseEvent, error)
			var responseTransformFailures []TransformFailure
			var responseTransformFailuresMu sync.Mutex
			responseTransformScopes := append([]TransformScope(nil), candidateScopes...)
			if k.ResponseTransforms.Active(transformBindings, responseTransformScopes...) {
				responseTransform = func(transformCtx context.Context, event ResponseEvent) (ResponseEvent, error) {
					updated, report, err := k.ResponseTransforms.ApplyScopesWithReport(transformCtx, event, transformBindings, responseTransformScopes...)
					if err == nil && len(report.Failures) > 0 {
						responseTransformFailuresMu.Lock()
						responseTransformFailures = append(responseTransformFailures, report.Failures...)
						responseTransformFailuresMu.Unlock()
					}
					return updated, err
				}
			}
			attemptReleased := false
			releaseAttempt := func() {
				if !attemptReleased {
					k.Scheduler.Release(candidate)
					attemptReleased = true
				}
			}
			var stagedCompletion *UsageEvent
			var providerUsage UsageEvent
			providerUsageKnown := false
			recordProviderUsage := func(event UsageEvent) {
				if knownProviderUsage(event) {
					providerUsage, providerUsageKnown = event, true
				}
			}
			var terminalFailure error
			reportFailure := func(err error) {
				if err == nil || terminalFailure != nil {
					return
				}
				terminalFailure = err
				if emitEvent != nil {
					emitEvent(ResponseEvent{At: time.Now(), Kind: EventResponseError, Error: err.Error()})
				}
				if providerResponseFailure(ctx, err) {
					cause := CauseStreamFailure
					if errors.Is(context.Cause(ctx), ErrOperationDeadline) {
						cause = CauseTimeout
					}
					if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
						observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorRetryable, Cause: cause, Effect: EffectAccepted, Scope: ScopeOperation, Retry: RetryAfter, Confidence: 0.9, Evidence: []EvidenceSource{EvidenceInferred}, Message: err.Error()})
					} else if feedback, ok := k.Scheduler.gate.(FeedbackGate); ok {
						feedback.MarkFailure(candidate, ErrorRetryable, err)
					}
				}
			}
			finishSuccess := func(event UsageEvent) {
				if observer, ok := k.Scheduler.gate.(OutcomeObserver); ok {
					observer.ObserveOutcome(candidate, ClassifiedOutcome{Class: ErrorTerminal, Cause: CauseSuccess, Effect: EffectAccepted, Scope: ScopeOperation, Retry: RetryNow, Confidence: 1, Evidence: []EvidenceSource{EvidenceSuccessBody}})
				}
				event.CompatibilityFidelity = compatibilityPlan.Fidelity
				event.CompatibilityLosses = cloneLossRecords(compatibilityPlan.Losses)
				responseTransformFailuresMu.Lock()
				compatibilityPlan.TransformFailures = append(compatibilityPlan.TransformFailures, responseTransformFailures...)
				responseTransformFailuresMu.Unlock()
				compatibilitySummary := SummarizeCompatibilityPlan(compatibilityPlan)
				event.CompatibilityPlan = &compatibilitySummary
				releaseAttempt()
				if sessionKey != "" && candidate.SessionStoreRef.ID != "" && (sessionState.ResponseID != "" || len(sessionState.ProviderData) > 0 || len(sessionState.Signatures) > 0) {
					if sessionStore := k.SessionStores[candidate.SessionStoreRef]; sessionStore != nil {
						_ = sessionStore.Save(ctx, candidate, sessionKey, sessionState)
					}
				}
				if source := k.UsageSources[candidate.UsageSourceRef]; source != nil {
					if enriched, enrichErr := source.EnrichUsage(ctx, candidate, response.Headers, event); enrichErr == nil {
						event = enriched
					}
				}
				event.Status = "ok"
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
			}
			renderErr := adapter.RenderResponse(attemptCtx, response, streamWriter, candidateRequest.SourceFormat, StreamHooks{Streaming: candidateRequest.Stream, Model: candidateRequest.Model, ProviderIssuer: candidate.DefinitionRef, MaxEventBytes: resourceBounds.MaxBufferedBytes, MaxOutputBytes: resourceBounds.MaxOutputBytes, TransformResponse: responseTransform, ArtifactAccess: responseArtifactAccess, OnError: func(streamErr error) {
				reportFailure(streamErr)
			}, ValidateEvent: validateEvent, OnEvent: func(event ResponseEvent) {
				if event.ResponseID != "" {
					sessionState.ResponseID = event.ResponseID
				}
				if emitEvent != nil && event.Kind != EventResponseComplete && event.Kind != EventResponseError {
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
			}, OnProviderUsage: recordProviderUsage, OnComplete: func(event UsageEvent) {
				recordProviderUsage(event)
				if stagedCompletion == nil {
					copy := event
					stagedCompletion = &copy
				}
			}})
			if terminalFailure != nil {
				renderErr = terminalFailure
			}
			if renderErr == nil && streamWriter.failure != nil {
				renderErr = streamWriter.failure
			}
			if renderErr == nil && resultProjector != nil && !resultFinalized {
				renderErr = WrapResponseFailure(ResponseFailureProvider, fmt.Errorf("operation %q did not finalize its declared result", operationRef.Key()))
			}
			if renderErr == nil && len(artifactOutputs) > 0 {
				if err := k.Operations.ValidateArtifactOutputCounts(operationRef, artifactOutputCounts); err != nil {
					renderErr = WrapResponseFailure(ResponseFailureProvider, err)
				}
			}
			if responseScope != nil {
				if closeErr := responseScope.Close(); renderErr == nil && closeErr != nil {
					renderErr = WrapResponseFailure(ResponseFailureRenderer, closeErr)
				}
			}
			if renderErr == nil && ctx.Err() != nil {
				renderErr = ctx.Err()
			}
			if renderErr == nil {
				if stagedCompletion != nil {
					finishSuccess(*stagedCompletion)
				}
				releaseAttempt()
				return nil
			}
			reportFailure(renderErr)
			releaseAttempt()
			failedUsage := providerUsage
			if source := k.UsageSources[candidate.UsageSourceRef]; source != nil {
				if enriched, err := source.EnrichUsage(ctx, candidate, response.Headers, failedUsage); err == nil {
					failedUsage = enriched
				}
			}
			if providerUsageKnown || knownProviderUsage(failedUsage) {
				event := failedUsage
				event.Status = "error"
				if ctx.Err() != nil && !errors.Is(context.Cause(ctx), ErrOperationDeadline) {
					event.Status = "cancelled"
				}
				event.At, event.LogicalModel, event.ProviderNodeID, event.ConnectionID, event.ExternalModel = time.Now(), req.Model, candidate.NodeID, candidate.CredentialID, candidate.ExternalModel
				event.Latency, event.RequestClass, event.SessionID = time.Since(started), requestClass(req), req.Session.ID
				k.EmitUsage(event)
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
	if event.SignatureIssuer.ID != "" {
		content["signatureIssuer"] = event.SignatureIssuer.Key()
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
	if len(event.Artifacts) > 0 {
		metadata := make([]map[string]any, 0, len(event.Artifacts))
		for _, artifact := range event.Artifacts {
			metadata = append(metadata, map[string]any{
				"typeRef": artifact.TypeRef, "role": artifact.Role, "mediaType": artifact.MediaType,
				"sizeBytes": artifact.SizeBytes, "sensitivity": artifact.Sensitivity,
			})
		}
		content["artifacts"] = metadata
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
	class := struct {
		Operation                                extensions.Ref   `json:"operation"`
		Format                                   normalize.Format `json:"format"`
		Stream, Vision, Audio, Video, PDF, Tools bool
		Thinking, Context                        string
	}{extensions.Ref{Kind: "operation", ID: string(req.Operation), ContractVersion: req.OperationContractVersion}, req.SourceFormat, req.Stream, req.Modalities.Vision, req.Modalities.AudioInput, req.Modalities.VideoInput, req.Modalities.PDF, len(req.Tools) > 0, req.Thinking.Effort, contextBucket}
	encoded, _ := json.Marshal(class)
	return string(encoded)
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
		outcome.Scope = ScopeConnection
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

func deriveIssuerIdempotencyKey(clientKey string, route Route, operation extensions.Ref) string {
	hasher := sha256.New()
	for _, part := range []string{"gobroom/idempotency/v1", clientKey, route.DefinitionRef.Key(), route.NodeID, route.BaseURL, route.CredentialID, route.ExternalModel, string(route.Protocol), operation.Key()} {
		_, _ = hasher.Write([]byte(part))
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func kernelErrorClass(err error) ErrorClass {
	if err == nil {
		return ErrorRetryable
	}
	return ErrorCooldown
}
