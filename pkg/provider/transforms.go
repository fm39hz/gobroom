package provider

import "github.com/fm39hz/gobroom/internal/kernel"

// These are the same catalog-bound contracts used by builtin transforms.
// Static external modules register implementations; bindings remain daemon data.
type RequestTransform = kernel.RequestTransform
type ResponseTransform = kernel.ResponseTransform
type TransformDefinition = kernel.TransformDefinition
type ResponseTransformDefinition = kernel.ResponseTransformDefinition
type TransformEffect = kernel.TransformEffect
type ResponseTransformEffect = kernel.ResponseTransformEffect
type TransformStage = kernel.TransformStage
type TransformScope = kernel.TransformScope
type TransformScopeKind = kernel.TransformScopeKind
type TransformBinding = kernel.TransformBinding
type TransformFailureMode = kernel.TransformFailureMode
type TransformFailureReason = kernel.TransformFailureReason
type TransformFailure = kernel.TransformFailure
type RequestTransformPlanStep = kernel.RequestTransformPlanStep
type ResponseTransformPlanStep = kernel.ResponseTransformPlanStep

const (
	RequestTransformKind        = kernel.RequestTransformKind
	ResponseTransformKind       = kernel.ResponseTransformKind
	TransformBeforeRequirements = kernel.TransformBeforeRequirements
	TransformPrompt             = kernel.TransformPrompt
	TransformInput              = kernel.TransformInput
	TransformTools              = kernel.TransformTools
	TransformThinking           = kernel.TransformThinking
	TransformOptions            = kernel.TransformOptions
	TransformContinuity         = kernel.TransformContinuity
	ResponseEffectText          = kernel.ResponseEffectText
	ResponseEffectThinking      = kernel.ResponseEffectThinking
	ResponseEffectToolArguments = kernel.ResponseEffectToolArguments
	ResponseEffectUsage         = kernel.ResponseEffectUsage
	TransformScopeDaemon        = kernel.TransformScopeDaemon
	TransformScopeModel         = kernel.TransformScopeModel
	TransformScopeRoute         = kernel.TransformScopeRoute
	TransformScopeProvider      = kernel.TransformScopeProvider
	TransformScopeConnection    = kernel.TransformScopeConnection
	TransformFailClosed         = kernel.TransformFailClosed
	TransformSafeFailOpen       = kernel.TransformSafeFailOpen
	TransformFailureApply       = kernel.TransformFailureApply
	TransformFailureEffect      = kernel.TransformFailureEffect
	TransformFailureBudget      = kernel.TransformFailureBudget
	TransformFailureDeadline    = kernel.TransformFailureDeadline
)

var ErrTransformSafetyViolation = kernel.ErrTransformSafetyViolation
