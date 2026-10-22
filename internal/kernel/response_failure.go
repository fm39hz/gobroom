package kernel

import (
	"context"
	"errors"
)

var ErrOperationDeadline = errors.New("operation execution deadline exceeded")

// ResponseFailureOrigin is evidence about the failing boundary, not a retry
// grant. An accepted upstream effect remains accepted even for a local failure.
type ResponseFailureOrigin string

const (
	ResponseFailureUnknown   ResponseFailureOrigin = "unknown"
	ResponseFailureProvider  ResponseFailureOrigin = "provider"
	ResponseFailureTransform ResponseFailureOrigin = "transform"
	ResponseFailureRenderer  ResponseFailureOrigin = "renderer"
	ResponseFailureClient    ResponseFailureOrigin = "client"
)

type ResponseFailure struct {
	Origin ResponseFailureOrigin
	Err    error
}

func (e *ResponseFailure) Error() string {
	if e == nil || e.Err == nil {
		return "response failed"
	}
	return e.Err.Error()
}

func (e *ResponseFailure) Unwrap() error { return e.Err }

// WrapResponseFailure keeps the earliest known boundary. In particular, a
// renderer/decoder wrapping a client-write error cannot relabel it upstream.
func WrapResponseFailure(origin ResponseFailureOrigin, err error) error {
	if err == nil {
		return nil
	}
	var existing *ResponseFailure
	if errors.As(err, &existing) {
		return err
	}
	return &ResponseFailure{Origin: origin, Err: err}
}

func responseFailureOrigin(err error) ResponseFailureOrigin {
	var failure *ResponseFailure
	if errors.As(err, &failure) {
		return failure.Origin
	}
	return ResponseFailureUnknown
}

func providerResponseFailure(ctx context.Context, err error) bool {
	if responseFailureOrigin(err) != ResponseFailureProvider {
		return false
	}
	return ctx.Err() == nil || errors.Is(context.Cause(ctx), ErrOperationDeadline)
}

func knownProviderUsage(event UsageEvent) bool {
	return event.HasReportedUsage()
}
