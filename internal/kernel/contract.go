package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

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
	ID                   string
	NodeID               string
	DisplayPrefix        string
	ExternalModel        string
	Protocol             Protocol
	AdapterID            string
	DefinitionID         string
	AuthFlowID           string
	ErrorClassifierID    string
	QuotaSourceID        string
	QuotaEndpointID      string
	QuotaTransportID     string
	QuotaEndpointOptions EndpointOptions
	QuotaWindowName      string
	UsageSourceID        string
	UsageOptions         UsageSourceOptions
	SessionStoreID       string
	Profile              CapabilityProfile
	Limits               TokenLimits
	Weight               int
	Enabled              bool
	BaseURL              string
	CredentialID         string
	CredentialType       string
}

type Snapshot struct {
	Version      uint64
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
}

type ResolvedModel struct {
	PublicName  string
	TargetRef   string
	Strategy    Strategy
	StickyLimit int
	Candidates  []Route
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
	StickyLimit            int
	Members                []MemberRef
	Identity               PhysicalIdentity
	Reasoning              NormalizedRequestReasoning
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

type UpstreamRequest struct {
	Method  string
	URL     string
	Headers http.Header
	Body    io.Reader
}

type UpstreamResponse struct {
	Status      int
	Headers     http.Header
	Body        io.ReadCloser
	FirstByteAt time.Time
}

type ErrorClass string

const (
	ErrorTerminal  ErrorClass = "terminal"
	ErrorRetryable ErrorClass = "retryable"
	ErrorCooldown  ErrorClass = "cooldown"
	ErrorAuth      ErrorClass = "auth"
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
	ScopeRouteConnection OutcomeScope = "route_connection"
	ScopeConnection      OutcomeScope = "connection"
	ScopeProvider        OutcomeScope = "provider"
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
	Scope      OutcomeScope
	Retry      RetryAction
	RetryAt    time.Time
	Confidence float64
	Evidence   []EvidenceSource
	Limits     []LimitWindow
	StatusCode int
	Message    string
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
	Protocol() Protocol
	Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error)
	Execute(context.Context, UpstreamRequest) (UpstreamResponse, error)
	ClassifyError(status int, body []byte) ErrorClass
	TranslateStream(context.Context, UpstreamResponse, http.ResponseWriter, normalize.Format, StreamHooks) error
}

// ClientFormatNegotiator declares which ingress contracts an adapter can
// render. Route eligibility belongs to this capability, not a kernel protocol
// switch. An adapter without a declaration is treated as externally managed
// (useful for test/custom adapters); production composed codecs must declare it.
type ClientFormatNegotiator interface {
	NegotiateClientFormat(normalize.Format) CompatibilityDecision
}

type CompatibilityDecision struct {
	Supported bool
	Lossless  bool
	Reason    string
}

// RequestHook runs on normalized semantics before a route adapter encodes the
// provider dialect. Hooks must not mutate shared state outside the request.
type RequestHook interface {
	ID() string
	Apply(context.Context, *NormalizedRequest) error
}

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
}

type CredentialResolver func(context.Context, Route) (Credential, error)
type CredentialRefresher func(context.Context, Route, Credential) (Credential, error)

type StreamHooks struct {
	OnFirstByte    func(time.Time)
	OnEvent        func(ResponseEvent)
	OnComplete     func(UsageEvent)
	OnError        func(error)
	OnSessionState func(SessionState)
}

type ResponseEventKind string

const (
	EventResponseStarted   ResponseEventKind = "response_started"
	EventContentBlockStart ResponseEventKind = "content_block_start"
	EventContentBlockEnd   ResponseEventKind = "content_block_end"
	EventTextDelta         ResponseEventKind = "text_delta"
	EventThinkingDelta     ResponseEventKind = "thinking_delta"
	EventToolCallDelta     ResponseEventKind = "tool_call_delta"
	EventUsage             ResponseEventKind = "usage"
	EventResponseComplete  ResponseEventKind = "response_complete"
	EventResponseError     ResponseEventKind = "response_error"
)

type ResponseEvent struct {
	At            time.Time
	Kind          ResponseEventKind
	Index         int
	ResponseID    string
	ItemID        string
	ContentType   string
	BlockType     string
	StopReason    string
	Text          string
	ToolCallID    string
	ToolName      string
	ToolArguments string
	Usage         *UsageEvent
	Error         string
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
	ResponseID   string          `json:"responseId,omitempty"`
	ProviderData json.RawMessage `json:"providerData,omitempty"`
	ExpiresAt    time.Time       `json:"-"`
}

type SessionStore interface {
	ID() string
	Load(context.Context, Route, string) (SessionState, bool, error)
	Save(context.Context, Route, string, SessionState) error
}

var (
	ErrModelNotPublished = errors.New("model is not published")
	ErrNoRoute           = errors.New("no usable route")
	ErrSnapshotInvalid   = errors.New("invalid route snapshot")
)
