package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/artifacts"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolAnthropic       Protocol = "anthropic"
	ProtocolGemini          Protocol = "gemini"
)

type Strategy string

const (
	StrategyFallback           Strategy = "fallback"
	StrategyRotatingFallback   Strategy = "rotating_fallback"
	StrategyRoundRobin         Strategy = "round_robin"
	StrategyRoundRobinFallback Strategy = "round_robin_fallback"
	StrategyWeighted           Strategy = "weighted"
)

type PublicModel struct {
	Name      string
	TargetRef string
	OwnedBy   string
}

type Route struct {
	ID                     string
	NodeID                 string
	DisplayPrefix          string
	ExternalModel          string
	Protocol               Protocol
	OperationBindings      map[normalize.Operation]RouteOperationBinding
	DefinitionID           string
	DefinitionRef          extensions.Ref
	AuthFlowID             string
	ErrorClassifierRef     extensions.Ref
	QuotaSourceRef         extensions.Ref
	QuotaEndpointRef       extensions.Ref
	QuotaTransportRef      extensions.Ref
	QuotaEndpointOptions   EndpointOptions
	QuotaWindowName        string
	UsageSourceRef         extensions.Ref
	UsageOptions           UsageSourceOptions
	SessionStoreRef        extensions.Ref
	SessionOperationRef    extensions.Ref `json:"-"` // exact operation selected for this request, not snapshot configuration
	Profile                CapabilityProfile
	Limits                 TokenLimits
	Weight                 int
	Enabled                bool
	BaseURL                string
	CredentialID           string
	CredentialType         string
	CredentialPresentation CredentialPresentation
	HealthGeneration       RouteHealthGeneration `json:"-"`
}

type RouteHealthGeneration struct {
	Provider   string
	Connection string
	Model      string
}

func (g RouteHealthGeneration) For(scope OutcomeScope) string {
	switch scope {
	case ScopeProvider:
		return g.Provider
	case ScopeConnection:
		return g.Provider + "\x00" + g.Connection
	default:
		return g.Provider + "\x00" + g.Connection + "\x00" + g.Model
	}
}

func (g RouteHealthGeneration) ForQuota(connectionID, modelRef string) string {
	switch {
	case connectionID == "" && modelRef == "":
		return g.Provider
	case connectionID == "" && modelRef != "":
		return g.Provider + "\x00" + modelRef
	case connectionID != "" && modelRef == "":
		return g.Provider + "\x00" + g.Connection
	default:
		return g.For(ScopeRoute)
	}
}

type RouteOperationBinding struct {
	ContractVersion    uint64
	AdapterIDs         []string
	Idempotency        map[string]IdempotencyPolicy
	ErrorClassifierRef extensions.Ref
	UsageSourceRef     extensions.Ref
	UsageOptions       UsageSourceOptions
	SessionStoreRef    extensions.Ref
}

type Snapshot struct {
	Version      uint64
	LossCeiling  LossPolicyCeiling
	PublicModels map[string]PublicModel
	Routes       map[string]Route
	// RouteGroups expands a logical catalog route into one candidate per
	// connection. The group key remains the stable model/catalog ID used by
	// combo members; variant IDs are internal to the data plane.
	RouteGroups map[string][]string
	// WireRoutes maps prefix/model references to route variants. It is only
	// reachable through a published target or combo member.
	WireRoutes map[string][]string
	// Nodes is the typed execution graph used by the data plane.
	Nodes map[string]ModelNode
	// TransformBindings are declarative, request-pinned module bindings.
	TransformBindings []TransformBinding
}

type ResolvedModel struct {
	PublicName     string
	TargetRef      string
	Strategy       Strategy
	StrategyRef    extensions.Ref
	StrategyConfig json.RawMessage
	StickyLimit    int
	Candidates     []Route
}

type MemberKind string

const (
	MemberModel      MemberKind = "model"
	MemberRoute      MemberKind = "route"
	MemberRouteGroup MemberKind = "route_group"
)

// MemberRef keeps model-policy boundaries distinct from provider routes.
type MemberRef struct {
	Kind     MemberKind
	ID       string
	Weight   int
	Fidelity SourceFidelity
	Evidence []Evidence
}

type ModelNodeKind string

const (
	ModelPhysical ModelNodeKind = "physical"
	ModelCombo    ModelNodeKind = "combo"
)

type ModelNode struct {
	ID                     string
	Kind                   ModelNodeKind
	Strategy               Strategy
	StrategyRef            extensions.Ref
	StrategyConfig         json.RawMessage
	StickyLimit            int
	Members                []MemberRef
	Identity               PhysicalIdentity
	Reasoning              NormalizedRequestReasoning
	LossPolicy             LossPolicy
	AllowCompatibleSources bool
	AllowDynamicSources    bool
}

type NormalizedRequestReasoning = normalize.ThinkingIntent

// StrategyPrimitive plans members at exactly one node boundary. Mutable
// state is scoped by the scheduler to that node ID.
type StrategyPrimitive interface {
	Plan(nodeID string, members []MemberRef, state *StrategyState) []MemberRef
	OnFailure(StrategyFailure, *StrategyState) FailureAction
}

type StrategyState struct {
	Cursor      int
	StickyIndex int
	StickyCount int
	StickyLimit int
	Config      json.RawMessage
}

type StrategyFailure struct {
	NodeID string
	Member MemberRef
	Class  ErrorClass
	Err    error
}

type FailureAction uint8

const (
	FailureContinue FailureAction = iota
	FailureStop
)

type NormalizedRequest = normalize.Request
type Message = normalize.Message
type Tool = normalize.Tool
type FeatureRequirement = normalize.FeatureRequirement

type UpstreamRequest struct {
	Method  string
	URL     string
	Headers http.Header
	Body    io.Reader
	Replay  PreparedRequestReplay
}

// PreparedRequestReplay opens the same business request with fresh body handles
// and authorization. It must not rerun semantic encoding or change the action.
type PreparedRequestReplay func(context.Context, Credential) (UpstreamRequest, error)

// PreparedAuthorization may read the frozen body to sign it, but cannot mutate
// its bytes. Each call receives an independent reader and cloned header map.
type PreparedAuthorization func(context.Context, http.Header, io.Reader, Credential) error

type UpstreamResponse struct {
	Status      int
	Headers     http.Header
	Body        io.ReadCloser
	FirstByteAt time.Time
}

type ErrorClass string

const (
	ErrorTerminal   ErrorClass = "terminal"
	ErrorRetryable  ErrorClass = "retryable"
	ErrorCooldown   ErrorClass = "cooldown"
	ErrorAuth       ErrorClass = "auth"
	ErrorCapability ErrorClass = "capability"
)

// OutcomeCause is deliberately more specific than ErrorClass. ErrorClass is
// kept as the compatibility decision used by existing strategies; Cause is
// the evidence the runtime uses for health, limits and user-facing status.
type OutcomeCause string

const (
	CauseSuccess        OutcomeCause = "success"
	CauseRequestInvalid OutcomeCause = "request_invalid"
	CauseCapability     OutcomeCause = "capability_mismatch"
	CauseModelNotFound  OutcomeCause = "model_not_found"
	CauseAuth           OutcomeCause = "auth"
	CausePermission     OutcomeCause = "permission"
	CauseQuotaExhausted OutcomeCause = "quota_exhausted"
	CauseRateLimited    OutcomeCause = "rate_limited"
	CauseCapacity       OutcomeCause = "capacity"
	CauseOverloaded     OutcomeCause = "overloaded"
	CauseTimeout        OutcomeCause = "timeout"
	CauseNetwork        OutcomeCause = "network"
	CauseProtocol       OutcomeCause = "protocol"
	CauseStreamFailure  OutcomeCause = "stream_failure"
	CauseUnknown        OutcomeCause = "unknown"
)

type OutcomeScope string

const (
	ScopeRequest         OutcomeScope = "request"
	ScopeRoute           OutcomeScope = "route"
	ScopeOperation       OutcomeScope = "operation"
	ScopeRouteConnection OutcomeScope = "route_connection"
	ScopeConnection      OutcomeScope = "connection"
	ScopeProvider        OutcomeScope = "provider"
)

type UpstreamEffect string

const (
	EffectUnknown       UpstreamEffect = "unknown"
	EffectNotDispatched UpstreamEffect = "not_dispatched"
	EffectRejected      UpstreamEffect = "confirmed_rejection"
	EffectAccepted      UpstreamEffect = "accepted"
)

type EvidenceSource string

const (
	EvidenceResponseHeader EvidenceSource = "response_header"
	EvidenceSuccessBody    EvidenceSource = "success_body"
	EvidenceErrorBody      EvidenceSource = "error_body"
	EvidenceInferred       EvidenceSource = "inferred"
)

type RetryAction string

const (
	RetryNow     RetryAction = "retry_now"
	RetryAfter   RetryAction = "retry_after"
	RetryNever   RetryAction = "retry_never"
	RetryUnknown RetryAction = "retry_unknown"
)

// LimitWindow is an observation from a real provider response. Pointers are
// intentional: zero and unknown are different states.
type LimitWindow struct {
	Name      string
	Kind      string
	Limit     *float64
	Used      *float64
	Remaining *float64
	ResetAt   *time.Time
	Source    EvidenceSource
}

type ClassifiedOutcome struct {
	Class      ErrorClass
	Cause      OutcomeCause
	Effect     UpstreamEffect
	Scope      OutcomeScope
	Retry      RetryAction
	RetryAt    time.Time
	Confidence float64
	Evidence   []EvidenceSource
	Limits     []LimitWindow
	StatusCode int
	Message    string
}

type ReplaySuppressedError struct {
	Operation normalize.Operation
	Safety    string
	Effect    UpstreamEffect
	Cause     error
}

func (e *ReplaySuppressedError) Error() string {
	if e == nil {
		return "operation replay was suppressed"
	}
	message := fmt.Sprintf("operation %q replay suppressed after upstream effect %q (safety %q)", e.Operation, e.Effect, e.Safety)
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *ReplaySuppressedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type OutcomeObserver interface {
	ObserveOutcome(Route, ClassifiedOutcome)
}

type UsageObserver interface {
	ObserveUsage(Route, UsageEvent)
}

type RouteRanker interface {
	RankRoutes([]Route, time.Time) []Route
}

type RequestClassRanker interface {
	RankRoutesFor([]Route, time.Time, string) []Route
}

type SessionRanker interface {
	RankRoutesForSession([]Route, time.Time, string, string) []Route
}

type RouteAdmission interface {
	Acquire(Route, time.Time) bool
	Release(Route)
}

type ProviderAdapter interface {
	ID() string
	PlanCompatibility(CompatibilityContext) CompatibilityPlan
	Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error)
	Execute(context.Context, UpstreamRequest) (UpstreamResponse, error)
	ClassifyError(status int, body []byte) ErrorClass
	RenderResponse(context.Context, UpstreamResponse, http.ResponseWriter, normalize.Format, StreamHooks) error
}

type CompatibilityContext struct {
	Request                 NormalizedRequest
	Route                   Route
	Operation               normalize.Operation
	Requirements            RequestRequirements
	Policy                  CompatibilityPolicy
	ActiveResponseTransform bool
	RequestTransformSteps   []RequestTransformPlanStep
	ResponseTransformSteps  []ResponseTransformPlanStep
	ArtifactTransfers       []ArtifactTransfer
	ArtifactOutputs         []extensions.ArtifactOutput
	TransformFailures       []TransformFailure
	ArtifactOutputRequired  bool
}

type CompatibilityFidelity string

const (
	FidelityNative      CompatibilityFidelity = "native"
	FidelityTranslated  CompatibilityFidelity = "translated"
	FidelityLossy       CompatibilityFidelity = "lossy"
	FidelityUnsupported CompatibilityFidelity = "unsupported"
)

type ErrorClassifier interface {
	ClassifyError(status int, body []byte) ErrorClass
}

type OutcomeClassifier interface {
	ClassifyOutcome(status int, headers http.Header, body []byte) ClassifiedOutcome
}

type Credential struct {
	ConnectionID string
	Type         string
	Secret       string
	RefreshToken string
	ExpiresAt    time.Time
	ClientID     string
	ClientSecret string
	// RequestHeaders are credential-scoped values produced by an auth flow at
	// resolution time. They are runtime-only and never persisted or serialized.
	RequestHeaders http.Header `json:"-"`
	// AuthContext carries bounded provider-specific state derived by the selected
	// auth flow (for example userId or machineId) into request codecs/transports.
	// It is runtime-only and never persisted or serialized by the kernel.
	AuthContext AuthContext `json:"-"`
}

// AuthContext carries string values from a provider auth implementation into
// its request codec or transport. Keys are namespaced by the provider
// implementation, not interpreted by the kernel.
type AuthContext map[string]string

const (
	maxAuthContextEntries    = 64
	maxAuthContextKeyBytes   = 128
	maxAuthContextValueBytes = 8192
	maxAuthContextBytes      = 32 * 1024
)

// ValidateAuthContext enforces bounded runtime-only provider state before any
// request primitive consumes it.
func (c Credential) ValidateAuthContext() error {
	if len(c.AuthContext) > maxAuthContextEntries {
		return fmt.Errorf("credential auth context exceeds %d entries", maxAuthContextEntries)
	}
	total := 0
	for key, value := range c.AuthContext {
		if strings.TrimSpace(key) == "" || len(key) > maxAuthContextKeyBytes {
			return fmt.Errorf("credential auth context contains an invalid key")
		}
		if strings.ContainsAny(key, "\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("credential auth context contains control characters")
		}
		if len(value) > maxAuthContextValueBytes {
			return fmt.Errorf("credential auth context value for %q exceeds %d bytes", key, maxAuthContextValueBytes)
		}
		total += len(key) + len(value)
		if total > maxAuthContextBytes {
			return fmt.Errorf("credential auth context exceeds %d bytes", maxAuthContextBytes)
		}
	}
	return nil
}

type CredentialResolver func(context.Context, Route) (Credential, error)
type CredentialRefresher func(context.Context, Route, Credential) (Credential, error)

type StreamHooks struct {
	OnFirstByte       func(time.Time)
	OnEvent           func(ResponseEvent)
	ValidateEvent     func(context.Context, ResponseEvent) error
	OnComplete        func(UsageEvent)
	OnError           func(error)
	OnSessionState    func(SessionState)
	TransformResponse func(context.Context, ResponseEvent) (ResponseEvent, error)
	ArtifactAccess    *artifacts.Access
	Streaming         bool
	Model             string
	ProviderIssuer    extensions.Ref
	MaxEventBytes     int64
	MaxOutputBytes    int64
}

type ResponseEventKind string

const (
	EventResponseStarted   ResponseEventKind = "response_started"
	EventContentBlockStart ResponseEventKind = "content_block_start"
	EventContentBlockEnd   ResponseEventKind = "content_block_end"
	EventTextDelta         ResponseEventKind = "text_delta"
	EventThinkingDelta     ResponseEventKind = "thinking_delta"
	EventThinkingSignature ResponseEventKind = "thinking_signature"
	EventToolCallDelta     ResponseEventKind = "tool_call_delta"
	EventUsage             ResponseEventKind = "usage"
	EventArtifact          ResponseEventKind = "artifact"
	EventResponseComplete  ResponseEventKind = "response_complete"
	EventResponseError     ResponseEventKind = "response_error"
	EventRawFrame          ResponseEventKind = "raw_frame"
)

type ResponseEvent struct {
	At              time.Time
	Kind            ResponseEventKind
	Index           int
	ResponseID      string
	ItemID          string
	ContentType     string
	BlockType       string
	StopReason      string
	Text            string
	Signature       string
	SignatureIssuer extensions.Ref
	ToolCallID      string
	ToolName        string
	ToolArguments   string
	Usage           *UsageEvent
	Error           string
	WireFormat      normalize.Format
	Raw             []byte
	Opaque          json.RawMessage
	Artifacts       []extensions.ArtifactRef
}

type UsageEvent struct {
	At                    time.Time
	LogicalModel          string
	ProviderNodeID        string
	ExternalModel         string
	ConnectionID          string
	Status                string
	Latency               time.Duration
	FirstByteAt           time.Time
	TTFT                  time.Duration
	OutputTokensPerSecond float64
	RequestClass          string
	SessionID             string
	InputTokens           int64
	OutputTokens          int64
	EstimatedCost         float64
	CompatibilityFidelity CompatibilityFidelity     `json:"compatibilityFidelity,omitempty"`
	CompatibilityLosses   []LossRecord              `json:"compatibilityLosses,omitempty"`
	CompatibilityPlan     *CompatibilityPlanSummary `json:"compatibilityPlan,omitempty"`
}

type UsageSourceOptions struct {
	InputTokensHeader   string `json:"inputTokensHeader,omitempty"`
	OutputTokensHeader  string `json:"outputTokensHeader,omitempty"`
	EstimatedCostHeader string `json:"estimatedCostHeader,omitempty"`
}

type UsageEnricher interface {
	EnrichUsage(context.Context, Route, http.Header, UsageEvent) (UsageEvent, error)
}

type SessionState struct {
	ResponseID   string           `json:"responseId,omitempty"`
	ProviderData json.RawMessage  `json:"providerData,omitempty"`
	Signatures   []SignatureProof `json:"signatures,omitempty"`
	ExpiresAt    time.Time        `json:"-"`
}

// SessionStore contains opaque continuation state. Implementations must key
// entries with SessionStoreKeyFor so state cannot cross connection, exact
// provider-definition, exact operation, physical-model or client-session scope.
type SessionStore interface {
	ID() string
	Load(context.Context, Route, string) (SessionState, bool, error)
	Save(context.Context, Route, string, SessionState) error
}

var (
	ErrModelNotPublished       = errors.New("model is not published")
	ErrNoRoute                 = errors.New("no usable route")
	ErrOperationRequired       = errors.New("semantic operation is required")
	ErrSessionStateUnavailable = errors.New("provider session state unavailable")
	ErrSignatureIssuerMissing  = errors.New("signed response event has no provider issuer")
	ErrSignatureIssuerMismatch = errors.New("signed response event issuer does not match selected provider")
	ErrSignatureProofLimit     = errors.New("provider session signature proof limit exceeded")
	ErrSnapshotInvalid         = errors.New("invalid route snapshot")
)
