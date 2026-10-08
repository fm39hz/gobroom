package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

// RouteCompatibilityExplanation describes compatibility for one concrete
// route reached through one model-graph path. It is a dry-run: no credential
// resolution, admission lease, provider preparation or network call occurs.
type RouteCompatibilityExplanation struct {
	RouteID    string                   `json:"routeId"`
	ModelPath  []string                 `json:"modelPath"`
	Compatible bool                     `json:"compatible"`
	Reason     string                   `json:"reason,omitempty"`
	Plan       CompatibilityPlanSummary `json:"plan"`
}

type modelRoutePath struct {
	route         Route
	nodes         []ModelNode
	sourceAllowed bool
	sourceReason  string
}

// ExplainCompatibility preflights the supplied normalized request against
// each enabled public route without changing scheduler strategy state.
func (k *Kernel) ExplainCompatibility(ctx context.Context, model string, request NormalizedRequest) ([]RouteCompatibilityExplanation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := k.Snapshots.Load()
	public, ok := snapshot.PublicModels[model]
	if !ok {
		return nil, ErrModelNotPublished
	}
	request.Model = model
	if request.Operation == "" {
		request.Operation = normalize.OperationChatGenerate
	}
	if request.OperationContractVersion == 0 {
		request.OperationContractVersion = 1
	}
	externalRequirements := append([]normalize.FeatureRequirement(nil), request.Requirements...)
	prepared, err := k.Operations.Prepare(request)
	if err != nil {
		return nil, err
	}
	request.Requirements = mergeRequirements(externalRequirements, prepared.Requirements)
	bindings := cloneTransformBindings(snapshot.TransformBindings)
	scopes := []TransformScope{daemonTransformScope()}
	var transformFailures []TransformFailure
	if k.Transforms.Active(bindings, scopes...) {
		request = normalize.CloneRequest(request)
		report, err := k.Transforms.ApplyScopesWithReport(ctx, &request, bindings, scopes...)
		if err != nil {
			return nil, fmt.Errorf("daemon request transform: %w", err)
		}
		transformFailures = append(transformFailures, report.Failures...)
		prepared, err = k.Operations.Prepare(request)
		if err != nil {
			return nil, err
		}
		request.Requirements = mergeRequirements(externalRequirements, prepared.Requirements)
	}

	paths := collectModelRoutePaths(snapshot, public.TargetRef, nil, true, "", map[string]bool{})
	resolved, err := ResolvePublic(snapshot, model)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(resolved.Candidates))
	for _, route := range resolved.Candidates {
		enabled[route.ID] = true
	}
	result := make([]RouteCompatibilityExplanation, 0, len(paths))
	seenPath := make(map[string]bool, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !enabled[path.route.ID] {
			continue
		}
		modelPath := make([]string, 0, len(path.nodes))
		candidateRequest := normalize.CloneRequest(request)
		candidateTransformFailures := append([]TransformFailure(nil), transformFailures...)
		candidateScopes := append([]TransformScope(nil), scopes...)
		policy := CompatibilityPolicy{LossCeiling: cloneLossCeiling(snapshot.LossCeiling)}
		var preflightErr error
		for _, node := range path.nodes {
			modelPath = append(modelPath, node.ID)
			policy = AddLossPolicy(policy, node.ID, node.LossPolicy)
			if (candidateRequest.Thinking.Mode == "" || candidateRequest.Thinking.Mode == "inherit") && node.Reasoning.Mode != "" && node.Reasoning.Mode != "inherit" {
				candidateRequest.Thinking = node.Reasoning
			}
			scope := TransformScope{Kind: TransformScopeModel, ID: node.ID}
			candidateScopes = append(candidateScopes, scope)
			if k.Transforms.Active(bindings, scope) {
				report, applyErr := k.Transforms.ApplyScopesWithReport(ctx, &candidateRequest, bindings, scope)
				if applyErr != nil {
					preflightErr = fmt.Errorf("model %q request transform: %w", node.ID, applyErr)
					break
				}
				candidateTransformFailures = append(candidateTransformFailures, report.Failures...)
				prepared, prepareErr := k.Operations.Prepare(candidateRequest)
				if prepareErr != nil {
					preflightErr = prepareErr
					break
				}
				candidateRequest.Requirements = mergeRequirements(externalRequirements, prepared.Requirements)
			}
		}
		attemptScopes := []TransformScope{{Kind: TransformScopeRoute, ID: path.route.ID}}
		if path.route.NodeID != "" {
			attemptScopes = append(attemptScopes, TransformScope{Kind: TransformScopeProvider, ID: path.route.NodeID})
		}
		if path.route.CredentialID != "" {
			attemptScopes = append(attemptScopes, TransformScope{Kind: TransformScopeConnection, ID: path.route.CredentialID})
		}
		candidateScopes = append(candidateScopes, attemptScopes...)
		if preflightErr == nil && k.Transforms.Active(bindings, attemptScopes...) {
			candidateRequest = normalize.CloneRequest(candidateRequest)
			if report, applyErr := k.Transforms.ApplyScopesWithReport(ctx, &candidateRequest, bindings, attemptScopes...); applyErr != nil {
				preflightErr = fmt.Errorf("route %q request transform: %w", path.route.ID, applyErr)
			} else if prepared, prepareErr := k.Operations.Prepare(candidateRequest); prepareErr != nil {
				preflightErr = prepareErr
			} else {
				candidateTransformFailures = append(candidateTransformFailures, report.Failures...)
				candidateRequest.Requirements = mergeRequirements(externalRequirements, prepared.Requirements)
			}
		}

		candidate := cloneRoute(path.route)
		requirements := CompileRequirements(candidateRequest)
		plan := CompatibilityPlan{Supported: false, Fidelity: FidelityUnsupported}
		plan.RequestTransformSteps = k.Transforms.Plan(bindings, candidateScopes...)
		plan.ResponseTransformSteps = k.ResponseTransforms.Plan(bindings, candidateScopes...)
		plan.TransformFailures = append([]TransformFailure(nil), candidateTransformFailures...)
		reason := path.sourceReason
		if reason == "" && preflightErr != nil {
			reason = preflightErr.Error()
		}
		if reason == "" && preflightErr == nil {
			binding, exists := candidate.OperationBindings[requirements.Operation]
			if !exists || binding.ContractVersion != requirements.OperationContractVersion {
				reason = "route has no matching operation binding"
			} else {
				candidate.ErrorClassifierRef = binding.ErrorClassifierRef
				candidate.UsageSourceRef = binding.UsageSourceRef
				candidate.UsageOptions = binding.UsageOptions
				candidate.SessionStoreRef = binding.SessionStoreRef
				receiver := extensions.ArtifactOwner{
					Domain: "provider", IssuerRef: &candidate.DefinitionRef,
					ProviderDefinitionID: candidate.DefinitionID, ConnectionID: candidate.CredentialID,
					ModelIdentity: candidate.ExternalModel, ClientContract: string(candidateRequest.SourceFormat),
					SessionID: requestSessionKey(candidateRequest),
				}
				operationRef := extensions.Ref{Kind: "operation", ID: string(requirements.Operation), ContractVersion: requirements.OperationContractVersion}
				if err := k.Operations.ValidateArtifactsForConsumer(operationRef, candidateRequest.Artifacts, receiver, candidate.DefinitionRef); err != nil {
					reason = err.Error()
				} else if k.ArtifactStore == nil && hasBodyArtifacts(candidateRequest.Artifacts) {
					reason = "artifact body store is unavailable"
				} else {
					plan.ArtifactTransfers = PlanArtifactTransfers(candidateRequest, operationRef, candidate.DefinitionRef)
					if eligible, why := Eligible(candidate, requirements); !eligible {
						reason = why
					} else if eligible, why := k.Features.Evaluate(candidate, requirements.Features); !eligible {
						reason = why
					} else if _, plan = k.adapterForRoute(candidate, candidateRequest, requirements, bindings, candidateScopes, candidateTransformFailures, policy); !plan.Supported {
						reason = plan.Reason
					}
				}
			}
		}
		if reason == "" {
			reason = "request is compatible with this route"
		}
		if !plan.Supported {
			plan.Reason = reason
		}
		pathKey := path.route.ID + "\x00" + strings.Join(modelPath, "\x00")
		if seenPath[pathKey] {
			continue
		}
		seenPath[pathKey] = true
		result = append(result, RouteCompatibilityExplanation{
			RouteID: path.route.ID, ModelPath: modelPath,
			Compatible: path.sourceAllowed && preflightErr == nil && plan.Supported,
			Reason:     reason, Plan: SummarizeCompatibilityPlan(plan),
		})
	}
	return result, nil
}

func collectModelRoutePaths(snapshot Snapshot, nodeID string, nodes []ModelNode, sourceAllowed bool, sourceReason string, stack map[string]bool) []modelRoutePath {
	if stack[nodeID] {
		return nil
	}
	node, ok := snapshot.Nodes[nodeID]
	if !ok {
		return nil
	}
	stack[nodeID] = true
	defer delete(stack, nodeID)
	nodes = append(append([]ModelNode(nil), nodes...), cloneNode(node))
	var result []modelRoutePath
	for _, member := range node.Members {
		switch member.Kind {
		case MemberModel:
			result = append(result, collectModelRoutePaths(snapshot, member.ID, nodes, sourceAllowed, sourceReason, stack)...)
		case MemberRoute, MemberRouteGroup:
			memberAllowed := sourceAllowed
			memberReason := sourceReason
			if node.Kind == ModelPhysical && !PhysicalSourceAllowed(node, member) {
				memberAllowed = false
				memberReason = fmt.Sprintf("physical model %q excludes this source fidelity", node.ID)
			}
			for _, route := range resolveRouteMember(snapshot, member) {
				result = append(result, modelRoutePath{route: route, nodes: nodes, sourceAllowed: memberAllowed, sourceReason: memberReason})
			}
		}
	}
	return result
}
