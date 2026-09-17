package kernel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gorouter/gorouter/internal/normalize"
)

type Protocol string

const (
	ProtocolOpenAIChat      Protocol = "openai_chat"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolAnthropic       Protocol = "anthropic"
)

type Strategy string

const (
	StrategyFallback           Strategy = "fallback"
	StrategyRoundRobin         Strategy = "round_robin"
	StrategyRoundRobinFallback Strategy = "round_robin_fallback"
	StrategyWeighted           Strategy = "weighted"
)

type PublicModel struct {
	Name      string
	TargetRef string
	OwnedBy   string
}

type Combo struct {
	Name        string
	Strategy    Strategy
	StickyLimit int
	Members     []string
}

type Route struct {
	ID            string
	NodeID        string
	DisplayPrefix string
	ExternalModel string
	Protocol      Protocol
	Capabilities  map[string]bool
	Weight        int
	Enabled       bool
}

type Snapshot struct {
	Version       uint64
	PublicModels  map[string]PublicModel
	Combos        map[string]Combo
	Routes        map[string]Route
	LogicalModels map[string]string
}

type ResolvedModel struct {
	PublicName string
	TargetRef  string
	Strategy   Strategy
	Candidates []Route
}

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

type ProviderAdapter interface {
	ID() string
	Protocol() Protocol
	Prepare(context.Context, NormalizedRequest, Route, Credential) (UpstreamRequest, error)
	Execute(context.Context, UpstreamRequest) (UpstreamResponse, error)
	ClassifyError(status int, body []byte) ErrorClass
	TranslateStream(context.Context, UpstreamResponse, http.ResponseWriter, StreamHooks) error
}

type Credential struct {
	ConnectionID string
	Type         string
	Secret       string
}

type StreamHooks struct {
	OnFirstByte func(time.Time)
	OnComplete  func(UsageEvent)
	OnError     func(error)
}

type UsageEvent struct {
	At             time.Time
	LogicalModel   string
	ProviderNodeID string
	ExternalModel  string
	ConnectionID   string
	Status         string
	Latency        time.Duration
	InputTokens    int64
	OutputTokens   int64
	EstimatedCost  float64
}

var (
	ErrModelNotPublished = errors.New("model is not published")
	ErrNoRoute           = errors.New("no usable route")
	ErrSnapshotInvalid   = errors.New("invalid route snapshot")
)
