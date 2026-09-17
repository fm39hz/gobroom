package provider

import "context"

// Adapter is the protocol/provider boundary. A built-in provider can use a
// preset plus one of these adapters; custom OpenAI-compatible nodes use the
// generic adapters without needing code changes.
type Adapter interface {
	ID() string
	DiscoverModels(ctx context.Context, node Node, credential Credential) ([]Model, error)
	Prepare(ctx context.Context, request NormalizedRequest, route Route, credential Credential) (UpstreamRequest, error)
	ClassifyError(status int, body []byte) ErrorClass
}

type Node struct {
	ID, BaseURL, Protocol string
}

type Credential struct {
	Type, Value string
}

type Model struct {
	ID, DisplayName string
	Capabilities    map[string]bool
	Raw             map[string]any
}

type Route struct {
	NodeID, ExternalModel, Protocol string
	Capabilities                    map[string]bool
}

type NormalizedRequest struct {
	Model      string
	Messages   []map[string]any
	Stream     bool
	Tools      []map[string]any
	Extensions map[string]any
}

type UpstreamRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

type ErrorClass string

const (
	ErrorTerminal  ErrorClass = "terminal"
	ErrorRetryable ErrorClass = "retryable"
	ErrorCooldown  ErrorClass = "cooldown"
	ErrorAuth      ErrorClass = "auth"
)
