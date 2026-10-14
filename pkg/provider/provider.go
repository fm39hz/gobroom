// Package provider is the Go API for authoring statically linked GoBroom
// provider modules. Modules register reusable versioned operations and
// primitives; provider manifests bind exact primitive refs as data.
//
// The types below are aliases to the daemon's contracts. External module
// packages should import this public package rather than GoBroom's internal
// implementation packages.
package provider

import (
	"net/http"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	core "github.com/fm39hz/gobroom/internal/provider"
)

type RuntimeModule = core.RuntimeModule
type RuntimeModuleFunc = core.RuntimeModuleFunc
type ClientIngress = core.ClientIngress
type ClientIngressRoute = core.ClientIngressRoute
type JSONOperationIngress = core.JSONOperationIngress
type MultipartArtifactBinding = core.MultipartArtifactBinding
type MultipartOperationIngress = core.MultipartOperationIngress
type ModuleRef = extensions.ModuleRef
type RuntimeRegistry = core.RuntimeRegistry
type RuntimeBinding = core.RuntimeBinding
type PrimitiveRegistry = core.PrimitiveRegistry
type ProviderDefinition = core.ProviderDefinition
type Operation = core.Operation
type OperationBinding = core.OperationBinding
type PrimitiveKind = core.PrimitiveKind
type PrimitiveRef = core.PrimitiveRef
type CapabilitySet = core.CapabilitySet
type Capability = kernel.Capability
type SupportState = kernel.SupportState
type FeatureEvaluator = kernel.FeatureEvaluator

const (
	SupportUnknown     = kernel.SupportUnknown
	SupportUnsupported = kernel.SupportUnsupported
	SupportNative      = kernel.SupportNative
	SupportEmulated    = kernel.SupportEmulated
	SupportConditional = kernel.SupportConditional
)

const RuntimeModuleHostContractVersion = core.RuntimeModuleHostContractVersion
const MultipartRequestCodecID = core.MultipartRequestCodecID

type OAuthFlowOptions = core.OAuthFlowOptions
type AuthOptions = core.AuthOptions
type AuthModeDefinition = core.AuthModeDefinition
type AuthModeMetadata = core.AuthModeMetadata
type BoundAuthMode = core.BoundAuthMode
type AuthFlow = core.AuthFlow
type AuthInput = core.AuthInput
type AuthFlowFactory = core.AuthFlowFactory
type SetupField = core.SetupField
type SetupSchema = core.SetupSchema
type DefinitionMetadata = core.DefinitionMetadata
type OperationMetadata = core.OperationMetadata
type ModelSource = core.ModelSource
type ModelSourceInput = core.ModelSourceInput
type ModelDescriptor = core.ModelDescriptor
type ModelCatalogSnapshot = core.ModelCatalogSnapshot
type ModelCatalogEvidence = core.ModelCatalogEvidence
type StaticModelSourceOptions = core.StaticModelSourceOptions
type StaticModelCatalogSource = core.StaticModelCatalogSource
type QuotaRequest = core.QuotaRequest
type QuotaHTTPError = core.QuotaHTTPError
type QuotaSnapshot = core.QuotaSnapshot
type QuotaRoutingEffect = core.QuotaRoutingEffect
type HTTPJSONQuotaOptions = core.HTTPJSONQuotaOptions
type HTTPJSONQuotaReport = core.HTTPJSONQuotaReport
type HTTPJSONQuotaWindow = core.HTTPJSONQuotaWindow
type QuotaReportFailureMode = core.QuotaReportFailureMode
type UsageSource = core.UsageSource
type QuotaSource = core.QuotaSource
type QuotaScope = core.QuotaScope
type ScopedQuotaSource = core.ScopedQuotaSource
type QuotaSourceFactory = core.QuotaSourceFactory
type ErrorClassifier = core.ErrorClassifier

func QuotaFailureCode(err error) string { return core.QuotaFailureCode(err) }

type Ref = extensions.Ref
type ExtensionFactory = extensions.Factory
type ExtensionDescriptor = extensions.Descriptor
type ResourceBounds = extensions.ResourceBounds
type ArtifactInput = extensions.ArtifactInput
type ArtifactOutput = extensions.ArtifactOutput
type ArtifactRef = extensions.ArtifactRef
type ArtifactOwner = extensions.ArtifactOwner
type BodyRef = extensions.BodyRef
type ArtifactInterpreter = extensions.ArtifactInterpreter
type ArtifactInterpreterBinding = extensions.ArtifactInterpreterBinding
type ArtifactReplayScope = extensions.ArtifactReplayScope
type ArtifactSensitivity = extensions.ArtifactSensitivity
type ArtifactPolicy = extensions.ArtifactPolicy

type Endpoint = kernel.Endpoint
type Transport = kernel.Transport
type RequestCodec = kernel.RequestCodec
type ResponseDecoder = kernel.ResponseDecoder
type ResponseRenderer = kernel.ResponseRenderer
type ProviderAdapter = kernel.ProviderAdapter
type Credential = kernel.Credential
type AuthContext = kernel.AuthContext
type CredentialPresentation = kernel.CredentialPresentation
type Route = kernel.Route
type UpstreamRequest = kernel.UpstreamRequest
type UpstreamResponse = kernel.UpstreamResponse
type NormalizedRequest = kernel.NormalizedRequest
type CompatibilityContext = kernel.CompatibilityContext
type CompatibilityPlan = kernel.CompatibilityPlan
type FacetMapping = kernel.FacetMapping
type FacetDisposition = kernel.FacetDisposition
type ResponseEvent = kernel.ResponseEvent
type ResponseEventKind = kernel.ResponseEventKind
type StreamHooks = kernel.StreamHooks
type ErrorClass = kernel.ErrorClass
type ClassifiedOutcome = kernel.ClassifiedOutcome
type EndpointOptions = kernel.EndpointOptions
type Protocol = kernel.Protocol
type CapabilityProfile = kernel.CapabilityProfile
type TokenLimits = kernel.TokenLimits
type SessionStore = kernel.SessionStore
type SessionStoreKey = kernel.SessionStoreKey
type SessionState = kernel.SessionState
type SignatureProof = kernel.SignatureProof

// ApplyCredentialPresentation applies the selected auth mode's safe static
// headers, dynamic credential headers and token presentation to an upstream
// request header map. External request-codec modules should use this instead
// of hardcoding API-key/OAuth branches.
func ApplyCredentialPresentation(headers http.Header, credential Credential, presentation CredentialPresentation) error {
	return kernel.ApplyCredentialPresentation(headers, credential, presentation)
}

// SessionStoreKeyFor returns the exact connection/provider/operation/model/
// client-session scope required to isolate opaque provider continuation state.
func SessionStoreKeyFor(route Route, sessionID string) (SessionStoreKey, error) {
	return kernel.SessionStoreKeyFor(route, sessionID)
}

// ValidateSessionState applies the same size, JSON and context-bound signature
// proof checks used by the built-in session-store implementations.
func ValidateSessionState(state SessionState) error {
	return kernel.ValidateSessionState(state)
}

type Format = normalize.Format
type Task = normalize.Operation
type OperationRequest = normalize.Request

const (
	FormatOpenAIChat      = normalize.FormatOpenAIChat
	FormatOpenAIResponses = normalize.FormatOpenAIResponses
	FormatAnthropic       = normalize.FormatAnthropic
	FormatGemini          = normalize.FormatGemini
	FormatGeminiCLI       = normalize.FormatGeminiCLI
	FormatAntigravity     = normalize.FormatAntigravity
	OperationChatGenerate = normalize.OperationChatGenerate
)

type ArtifactFacts = normalize.ArtifactFacts
type FeatureRequirement = normalize.FeatureRequirement
type Message = normalize.Message
type Tool = normalize.Tool
type ToolCall = normalize.ToolCall
type PromptPlan = normalize.PromptPlan
type PromptLayer = normalize.PromptLayer
type ThinkingIntent = normalize.ThinkingIntent
type GenerationOptions = normalize.GenerationOptions
type Modalities = normalize.Modalities
type ContinuityState = normalize.ContinuityState
type OperationDefinition = operations.Definition
type OperationRegistry = operations.Registry
type OperationSnapshot = operations.Snapshot
type ArtifactBodyOpener = operations.ArtifactBodyOpener
type ResultProjector = operations.ResultProjector
type ResultProjectorFactory = operations.ResultProjectorFactory
type ReplaySafety = operations.ReplaySafety

const (
	OperationChat                  = core.OperationChat
	OperationResponses             = core.OperationResponses
	OperationMessages              = core.OperationMessages
	OperationModels                = core.OperationModels
	OperationEmbeddings            = core.OperationEmbeddings
	OperationImage                 = core.OperationImage
	OperationAudio                 = core.OperationAudio
	OperationVideo                 = core.OperationVideo
	OperationQuota                 = core.OperationQuota
	ModelCatalogConnectionObserved = core.ModelCatalogConnectionObserved
	ModelCatalogProviderRegistry   = core.ModelCatalogProviderRegistry
	QuotaReportsFailAll            = core.QuotaReportsFailAll
	QuotaReportsKeepAvailable      = core.QuotaReportsKeepAvailable
	QuotaScopeModel                = core.QuotaScopeModel
	QuotaScopeConnection           = core.QuotaScopeConnection
	QuotaRoutingEnforce            = core.QuotaRoutingEnforce
	QuotaRoutingObserve            = core.QuotaRoutingObserve

	PrimitiveEndpoint        = core.PrimitiveEndpoint
	PrimitiveTransport       = core.PrimitiveTransport
	PrimitiveAuth            = core.PrimitiveAuth
	PrimitiveRequestCodec    = core.PrimitiveRequestCodec
	PrimitiveResponseDecoder = core.PrimitiveResponseDecoder
	PrimitiveModelSource     = core.PrimitiveModelSource
	PrimitiveUsageSource     = core.PrimitiveUsageSource
	PrimitiveQuotaSource     = core.PrimitiveQuotaSource
	PrimitiveSessionStore    = core.PrimitiveSessionStore
	PrimitiveErrorClassifier = core.PrimitiveErrorClassifier
	PrimitiveExtension       = core.PrimitiveExtension

	ReplayBeforeDispatch = operations.ReplayBeforeDispatch
	ReplayOnRejection    = operations.ReplayOnRejection
	ReplayWithKey        = operations.ReplayWithKey
	ReplayNever          = operations.ReplayNever

	SchemaKind              = extensions.SchemaKind
	ArtifactKind            = extensions.ArtifactKind
	ArtifactInterpreterKind = extensions.ArtifactInterpreterKind
	ArtifactReplayRequest   = extensions.ArtifactReplayRequest
	ArtifactReplaySession   = extensions.ArtifactReplaySession
	ArtifactReplayPortable  = extensions.ArtifactReplayPortable
	ArtifactPublic          = extensions.ArtifactPublic
	ArtifactPrivate         = extensions.ArtifactPrivate
	ArtifactSecret          = extensions.ArtifactSecret

	FacetWireRequest      = kernel.FacetWireRequest
	FacetReasoningIntent  = kernel.FacetReasoningIntent
	FacetReasoningBudget  = kernel.FacetReasoningBudget
	FacetTranslated       = kernel.FacetTranslated
	FacetPreserved        = kernel.FacetPreserved
	EventTextDelta        = kernel.EventTextDelta
	EventResponseComplete = kernel.EventResponseComplete
)

func NewRuntimeRegistry() (*RuntimeRegistry, error) { return core.NewRuntimeRegistry() }

func NewRuntimeRegistryWithModules(modules ...RuntimeModule) (*RuntimeRegistry, error) {
	return core.NewRuntimeRegistryWithModules(modules...)
}

func ValidateClientIngress(codec ClientIngress) error { return core.ValidateClientIngress(codec) }

func ProviderDefinitionRef(id string, contractVersion uint64) Ref {
	return core.ProviderDefinitionRef(id, contractVersion)
}

func RuntimeBindingKey(definitionRef Ref, operation Operation) string {
	return core.RuntimeBindingKey(definitionRef, operation)
}

func EncodeManifest(definition ProviderDefinition) ([]byte, error) {
	return core.EncodeManifest(definition)
}
