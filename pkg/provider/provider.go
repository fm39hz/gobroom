// Package provider is the Go API for authoring statically linked GoBroom
// provider modules. Modules register reusable versioned operations and
// primitives; provider manifests bind exact primitive refs as data.
//
// The types below are aliases to the daemon's contracts. External module
// packages should import this public package rather than GoBroom's internal
// implementation packages.
package provider

import (
	"context"
	"encoding/json"
	"github.com/fm39hz/gobroom/internal/adapter/usagewire"
	"net/http"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	core "github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/quota"
)

type RuntimeModule = core.RuntimeModule
type RuntimeModuleFunc = core.RuntimeModuleFunc
type ClientIngress = core.ClientIngress
type ClientIngressRoute = core.ClientIngressRoute
type JSONOperationIngress = core.JSONOperationIngress
type ClientFailureContext = core.ClientFailureContext
type ClientFailureResponse = core.ClientFailureResponse
type FailureEncoder = core.FailureEncoder
type JSONFailureEncoder = core.JSONFailureEncoder
type AnthropicFailureEncoder = core.AnthropicFailureEncoder
type MultipartArtifactBinding = core.MultipartArtifactBinding
type MultipartOperationIngress = core.MultipartOperationIngress
type ModuleRef = extensions.ModuleRef
type RuntimeRegistry = core.RuntimeRegistry
type RuntimeBinding = core.RuntimeBinding
type PrimitiveRegistry = core.PrimitiveRegistry
type ProviderDefinition = core.ProviderDefinition
type Operation = core.Operation
type OperationBinding = core.OperationBinding
type PreparedRequestReplay = kernel.PreparedRequestReplay
type PreparedAuthorization = kernel.PreparedAuthorization
type IdempotencyPolicy = kernel.IdempotencyPolicy
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
type OAuthTokenState = core.OAuthTokenState

func OAuthAuthFactory(options AuthOptions) (AuthFlow, error) { return core.OAuthAuthFactory(options) }
func EncodeOAuthTokenState(state OAuthTokenState) (string, error) {
	return core.EncodeOAuthTokenState(state)
}
func DecodeOAuthTokenState(encoded string) (OAuthTokenState, error) {
	return core.DecodeOAuthTokenState(encoded)
}

type AuthOptions = core.AuthOptions
type AuthModeDefinition = core.AuthModeDefinition
type AuthModeMetadata = core.AuthModeMetadata
type BoundAuthMode = core.BoundAuthMode
type AuthFlow = core.AuthFlow
type AuthorizationCodeOptions = core.AuthorizationCodeOptions
type AuthorizationCodeRequest = core.AuthorizationCodeRequest
type AuthorizationCodeExchange = core.AuthorizationCodeExchange
type AuthorizationCodeInput = core.AuthorizationCodeInput
type OAuthSetupMode = core.OAuthSetupMode

const (
	OAuthSetupAuto   = core.OAuthSetupAuto
	OAuthSetupCode   = core.OAuthSetupCode
	OAuthSetupDevice = core.OAuthSetupDevice
)

var AuthorizationCodeInputSchemaRef = core.AuthorizationCodeInputSchemaRef

func NewAuthorizationCodeDriver(auth AuthFlow, options AuthorizationCodeOptions, authorize func(context.Context, AuthorizationCodeRequest) (string, error), exchange func(context.Context, AuthorizationCodeExchange) (CredentialState, error)) (AuthFlow, error) {
	return core.NewAuthorizationCodeDriver(auth, options, authorize, exchange)
}

type DeviceAuthorization = core.DeviceAuthorization
type AuthInput = core.AuthInput
type CredentialState = core.CredentialState
type AuthorizationFlow = core.AuthorizationFlow
type AuthorizationInputRejected = core.AuthorizationInputRejected
type AuthorizationState = core.AuthorizationState
type AuthorizationActionKind = core.AuthorizationActionKind
type AuthorizationAction = core.AuthorizationAction
type AuthorizationEventKind = core.AuthorizationEventKind
type AuthorizationEvent = core.AuthorizationEvent
type AuthorizationTransition = core.AuthorizationTransition
type AuthorizationCallback = core.AuthorizationCallback
type AuthorizationCallbackResource = core.AuthorizationCallbackResource
type AuthorizationCallbackMode = core.AuthorizationCallbackMode
type AuthorizationCallbackInput = core.AuthorizationCallbackInput
type AuthorizationFailure = core.AuthorizationFailure

const (
	AuthorizationDenied      = core.AuthorizationDenied
	AuthorizationExpired     = core.AuthorizationExpired
	AuthorizationUnavailable = core.AuthorizationUnavailable
)

const MaxCredentialStateBytes = core.MaxCredentialStateBytes
const MaxAuthorizationStateBytes = core.MaxAuthorizationStateBytes
const MaxAuthorizationDuration = core.MaxAuthorizationDuration

const (
	AuthorizationOpenURL              = core.AuthorizationOpenURL
	AuthorizationDisplayCode          = core.AuthorizationDisplayCode
	AuthorizationInput                = core.AuthorizationInput
	AuthorizationSubmit               = core.AuthorizationSubmit
	AuthorizationPoll                 = core.AuthorizationPoll
	AuthorizationCallbacksReady       = core.AuthorizationCallbacksReady
	AuthorizationCallbackLoopback     = core.AuthorizationCallbackLoopback
	AuthorizationCallbackProxiedHTTPS = core.AuthorizationCallbackProxiedHTTPS
	AuthorizationCallbackManual       = core.AuthorizationCallbackManual
	AuthorizationAwait                = core.AuthorizationAwait
)

func NewCredentialState(encoded string) (CredentialState, error) {
	return core.NewCredentialState(encoded)
}
func NewDeviceAuthorizationDriver(auth AuthFlow, begin func(context.Context, AuthInput) (DeviceAuthorization, error), await func(context.Context, DeviceAuthorization) (CredentialState, error)) (AuthFlow, error) {
	return core.NewDeviceAuthorizationDriver(auth, begin, await)
}
func NewAuthorizationState(encoded string) (AuthorizationState, error) {
	return core.NewAuthorizationState(encoded)
}

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
type QuotaIdentity = quota.Identity
type QuotaRoutingEffect = core.QuotaRoutingEffect
type HTTPJSONQuotaOptions = core.HTTPJSONQuotaOptions
type HTTPJSONQuotaReport = core.HTTPJSONQuotaReport
type HTTPJSONQuotaWindow = core.HTTPJSONQuotaWindow
type QuotaReportFailureMode = core.QuotaReportFailureMode
type UsageSource = core.UsageSource
type QuotaSource = core.QuotaSource
type HTTPJSONQuotaSource = core.HTTPJSONQuotaSource
type QuotaScope = core.QuotaScope
type ScopedQuotaSource = core.ScopedQuotaSource
type QuotaSourceFactory = core.QuotaSourceFactory
type ErrorClassifier = core.ErrorClassifier
type ResponseOutcomeClassifier = kernel.ResponseOutcomeClassifier
type ProviderResponseError = kernel.ProviderResponseError
type ErrorClassifierOptions = core.ErrorClassifierOptions
type HTTPJSONErrorClassifierOptions = core.HTTPJSONErrorClassifierOptions
type HTTPJSONErrorSignal = core.HTTPJSONErrorSignal
type HTTPJSONFieldMatch = core.HTTPJSONFieldMatch
type CredentialRecovery = kernel.CredentialRecovery
type ContinuityRecovery = kernel.ContinuityRecovery

const (
	CredentialRecoveryNone       = kernel.CredentialRecoveryNone
	CredentialRecoveryRefresh    = kernel.CredentialRecoveryRefresh
	ContinuityRecoveryNone       = kernel.ContinuityRecoveryNone
	ContinuityRecoveryInvalidate = kernel.ContinuityRecoveryInvalidate
	CauseContinuityInvalid       = kernel.CauseContinuityInvalid
)

type HTTPHeaderLimitSignal = core.HTTPHeaderLimitSignal
type OutcomeScope = kernel.OutcomeScope

const (
	ScopeRequest         = kernel.ScopeRequest
	ScopeRoute           = kernel.ScopeRoute
	ScopeRouteConnection = kernel.ScopeRouteConnection
	ScopeProvider        = kernel.ScopeProvider
	ScopeOperation       = kernel.ScopeOperation
	ScopeConnection      = kernel.ScopeConnection
)

func QuotaFailureCode(err error) string { return core.QuotaFailureCode(err) }
func NewProviderResponseError(data []byte) (*ProviderResponseError, error) {
	return kernel.NewProviderResponseError(data)
}

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
type UsageValue = extensions.UsageValue
type UsagePolicy = extensions.UsagePolicy
type UsageDisclosure = extensions.UsageDisclosure
type UsageDescriptorSnapshot = extensions.UsageDescriptorSnapshot

const (
	UsageKind              = extensions.UsageKind
	UsagePrivate           = extensions.UsagePrivate
	UsageManagementVisible = extensions.UsageManagementVisible
)

type Endpoint = kernel.Endpoint
type Transport = kernel.Transport
type RequestCodec = kernel.RequestCodec
type ResponseDecoder = kernel.ResponseDecoder
type ResponseRenderer = kernel.ResponseRenderer
type ResponseRenderContext = kernel.ResponseRenderContext
type ResponseRenderSession = kernel.ResponseRenderSession
type CompatibilityPolicy = kernel.CompatibilityPolicy
type ProviderAdapter = kernel.ProviderAdapter
type Credential = kernel.Credential
type CredentialLease = kernel.CredentialLease
type CredentialLeaseScope = kernel.CredentialLeaseScope
type AuthContext = kernel.AuthContext
type CredentialPresentation = kernel.CredentialPresentation
type Route = kernel.Route

const MaxCredentialLeaseDuration = kernel.MaxCredentialLeaseDuration

func CredentialLeaseScopeForRoute(route Route) CredentialLeaseScope {
	return kernel.CredentialLeaseScopeForRoute(route)
}

func CredentialLeaseScopeForQuota(route Route) CredentialLeaseScope {
	return kernel.CredentialLeaseScopeForQuota(route)
}

func CredentialLeaseScopeForConsumer(route Route, consumer Ref) CredentialLeaseScope {
	return kernel.CredentialLeaseScopeForConsumer(route, consumer)
}

func NewCredentialLease(scope CredentialLeaseScope, credential Credential, expiresAt time.Time, check func(context.Context) error, release func()) (*CredentialLease, error) {
	return kernel.NewCredentialLease(scope, credential, expiresAt, check, release)
}

type UpstreamRequest = kernel.UpstreamRequest
type UpstreamResponse = kernel.UpstreamResponse
type NormalizedRequest = kernel.NormalizedRequest
type CompatibilityContext = kernel.CompatibilityContext
type CompatibilityPlan = kernel.CompatibilityPlan
type FacetMapping = kernel.FacetMapping
type FacetDisposition = kernel.FacetDisposition
type ResponseEvent = kernel.ResponseEvent
type ResponseEventKind = kernel.ResponseEventKind
type UsageEvent = kernel.UsageEvent

var ErrSessionStateSuperseded = kernel.ErrSessionStateSuperseded

type UsageEvidence = kernel.UsageEvidence
type UsageRequirements = extensions.UsageRequirements
type UsageUpdateKind = kernel.UsageUpdateKind
type UsageIssue = kernel.UsageIssue
type UsageLimits = kernel.UsageLimits
type UsageCapturePolicy = kernel.UsageCapturePolicy
type RawUsageFragment = kernel.RawUsageFragment

func DefaultUsageCapturePolicy() UsageCapturePolicy { return kernel.DefaultUsageCapturePolicy() }
func SelectRawUsage(policy UsageCapturePolicy, definition extensions.Ref, api string, value json.RawMessage) (*RawUsageFragment, []UsageIssue) {
	return kernel.SelectRawUsage(policy, definition, api, value)
}

type UsageCounter = usagewire.Counter
type ReportedCharge = usagewire.ReportedCharge
type ChargeProvenance = usagewire.ChargeProvenance

func ReportedChargeRef() extensions.Ref { return usagewire.ReportedChargeRef() }
func ReportedChargeValue(amount json.RawMessage, currency *string, provenance ChargeProvenance) (extensions.UsageValue, error) {
	return usagewire.ChargeValue(amount, currency, provenance.Source, provenance.Field)
}

const (
	UsagePartialSnapshot = kernel.UsagePartialSnapshot
	UsageFullSnapshot    = kernel.UsageFullSnapshot
)

type ResponseFailureOrigin = kernel.ResponseFailureOrigin
type ResponseFailure = kernel.ResponseFailure

const (
	ResponseFailureUnknown   = kernel.ResponseFailureUnknown
	ResponseFailureProvider  = kernel.ResponseFailureProvider
	ResponseFailureTransform = kernel.ResponseFailureTransform
	ResponseFailureRenderer  = kernel.ResponseFailureRenderer
	ResponseFailureClient    = kernel.ResponseFailureClient
)

func WrapResponseFailure(origin ResponseFailureOrigin, err error) error {
	return kernel.WrapResponseFailure(origin, err)
}

type StreamHooks = kernel.StreamHooks
type ErrorClass = kernel.ErrorClass
type ClassifiedOutcome = kernel.ClassifiedOutcome
type ClientFailure = kernel.ClientFailure
type ClientFailureCode = kernel.ClientFailureCode
type ClientFailureDiagnostics = kernel.ClientFailureDiagnostics

const (
	ClientFailureUpstream          = kernel.ClientFailureUpstream
	ClientFailureContinuityRestart = kernel.ClientFailureContinuityRestart
	ClientFailureInvalidRequest    = kernel.ClientFailureInvalidRequest
	ClientFailureModelUnavailable  = kernel.ClientFailureModelUnavailable
	ClientFailureNoRoute           = kernel.ClientFailureNoRoute
)

var ErrContinuityRestartRequired = kernel.ErrContinuityRestartRequired

func ClientFailureForError(err error) ClientFailure { return kernel.ClientFailureForError(err) }
func ClientFailureForErrorWithFallback(err error, fallback ClientFailure) ClientFailure {
	return kernel.ClientFailureForErrorWithFallback(err, fallback)
}
func ClientFailureDiagnosticsForError(err error) ClientFailureDiagnostics {
	return kernel.ClientFailureDiagnosticsForError(err)
}
func NewClientFailureError(detail ClientFailure, cause error) error {
	return kernel.NewClientFailureError(detail, cause)
}

type OutcomeClassifier = kernel.OutcomeClassifier
type OutcomeCause = kernel.OutcomeCause
type UpstreamEffect = kernel.UpstreamEffect
type RetryAction = kernel.RetryAction
type EvidenceSource = kernel.EvidenceSource

const (
	ErrorTerminal          = kernel.ErrorTerminal
	ErrorRetryable         = kernel.ErrorRetryable
	ErrorCooldown          = kernel.ErrorCooldown
	ErrorAuth              = kernel.ErrorAuth
	ErrorCapability        = kernel.ErrorCapability
	EffectUnknown          = kernel.EffectUnknown
	EffectNotDispatched    = kernel.EffectNotDispatched
	EffectRejected         = kernel.EffectRejected
	EffectAccepted         = kernel.EffectAccepted
	RetryNow               = kernel.RetryNow
	RetryAfter             = kernel.RetryAfter
	RetryNever             = kernel.RetryNever
	RetryUnknown           = kernel.RetryUnknown
	EvidenceResponseHeader = kernel.EvidenceResponseHeader
	EvidenceSuccessBody    = kernel.EvidenceSuccessBody
	EvidenceErrorBody      = kernel.EvidenceErrorBody
	EvidenceInferred       = kernel.EvidenceInferred
)

type EndpointOptions = kernel.EndpointOptions
type Protocol = kernel.Protocol
type CapabilityProfile = kernel.CapabilityProfile
type TokenLimits = kernel.TokenLimits
type SessionStore = kernel.SessionStore
type SessionStoreKey = kernel.SessionStoreKey
type SessionStoreNamespace = kernel.SessionStoreNamespace

const SessionStoreNamespaceVersion = kernel.SessionStoreNamespaceVersion

func SessionStoreNamespaceFor(route Route) (SessionStoreNamespace, error) {
	return kernel.SessionStoreNamespaceFor(route)
}
func EncodeSessionStoreNamespace(route Route) (string, error) {
	return kernel.EncodeSessionStoreNamespace(route)
}
func DecodeSessionStoreNamespace(raw string) (SessionStoreNamespace, error) {
	return kernel.DecodeSessionStoreNamespace(raw)
}

type SessionState = kernel.SessionState
type SignatureProof = kernel.SignatureProof

func ComposeCompatibilityPlan(stages [][]FacetMapping, policy CompatibilityPolicy) CompatibilityPlan {
	return kernel.ComposeCompatibilityPlan(stages, policy)
}

func RequiredRequestFacets(request NormalizedRequest) []string {
	return kernel.RequiredRequestFacets(request)
}
func ResponseEventFacet(event ResponseEventKind) string { return kernel.ResponseEventFacet(event) }

// ApplyCredentialPresentation applies the selected auth mode's safe static
// headers, dynamic credential headers and token presentation to an upstream
// request header map. External request-codec modules should use this instead
// of hardcoding API-key/OAuth branches.
func ApplyCredentialPresentation(headers http.Header, credential Credential, presentation CredentialPresentation) error {
	return kernel.ApplyCredentialPresentation(headers, credential, presentation)
}

// PrepareBytesRequest separates one-time business encoding from renewable
// authorization, including body-bound signing through an independent reader.
func PrepareBytesRequest(ctx context.Context, method, url string, headers http.Header, body []byte, credential Credential, authorize PreparedAuthorization, replay bool) (UpstreamRequest, error) {
	return kernel.PrepareBytesRequest(ctx, method, url, headers, body, credential, authorize, replay)
}

func CredentialAuthorization(presentation CredentialPresentation) PreparedAuthorization {
	return kernel.CredentialAuthorization(presentation)
}

// ApplyPreparedRequestHeaders puts kernel-owned dispatch headers in place
// before a custom signer computes a header-bound signature.
func ApplyPreparedRequestHeaders(ctx context.Context, headers http.Header) {
	kernel.ApplyPreparedRequestHeaders(ctx, headers)
}

// PublishInlineResponseArtifact mints a declared, bounded provider output using
// the selected route's owner. Client delivery does not widen its replay scope.
func PublishInlineResponseArtifact(ctx context.Context, typeRef Ref, role, mediaType string, replayScope ArtifactReplayScope, sensitivity ArtifactSensitivity, value []byte, ttl time.Duration) (ArtifactRef, error) {
	return artifacts.PublishInlineResponseArtifactFromContext(ctx, typeRef, role, mediaType, replayScope, sensitivity, value, ttl)
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
type ContentPart = normalize.ContentPart
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

	SchemaKind               = extensions.SchemaKind
	ArtifactKind             = extensions.ArtifactKind
	ArtifactInterpreterKind  = extensions.ArtifactInterpreterKind
	ArtifactReplayRequest    = extensions.ArtifactReplayRequest
	ArtifactReplaySession    = extensions.ArtifactReplaySession
	ArtifactReplayConnection = extensions.ArtifactReplayConnection
	ArtifactReplayPortable   = extensions.ArtifactReplayPortable
	ArtifactPublic           = extensions.ArtifactPublic
	ArtifactPrivate          = extensions.ArtifactPrivate
	ArtifactSecret           = extensions.ArtifactSecret

	FacetWireRequest       = kernel.FacetWireRequest
	FacetWireResponse      = kernel.FacetWireResponse
	EventArtifact          = kernel.EventArtifact
	FacetReasoningIntent   = kernel.FacetReasoningIntent
	FacetReasoningBudget   = kernel.FacetReasoningBudget
	FacetTranslated        = kernel.FacetTranslated
	FacetPreserved         = kernel.FacetPreserved
	EventTextDelta         = kernel.EventTextDelta
	EventResponseComplete  = kernel.EventResponseComplete
	EventResponseStarted   = kernel.EventResponseStarted
	EventContentBlockStart = kernel.EventContentBlockStart
	EventContentBlockEnd   = kernel.EventContentBlockEnd
	EventThinkingDelta     = kernel.EventThinkingDelta
	EventThinkingSignature = kernel.EventThinkingSignature
	EventToolCallDelta     = kernel.EventToolCallDelta
	EventUsage             = kernel.EventUsage
	EventResponseError     = kernel.EventResponseError
	EventRawFrame          = kernel.EventRawFrame
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
