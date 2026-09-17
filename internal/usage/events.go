package usage

import "time"

// Event is the compact durable fact emitted after a request. It is consumed
// by quota accounting, cost/budget policy, route scoring and the control API.
// The dashboard is only one consumer.
type Event struct {
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

type Sink interface{ Publish(Event) error }

type Policy interface {
	Accept(Event) bool
}
