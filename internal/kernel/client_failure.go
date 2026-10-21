package kernel

import (
	"errors"
	"unicode/utf8"

	"github.com/fm39hz/gobroom/internal/operations"
)

type ClientFailureCode string

const (
	ClientFailureUpstream          ClientFailureCode = "upstream_error"
	ClientFailureContinuityRestart ClientFailureCode = "continuity_restart_required"
	ClientFailureInvalidRequest    ClientFailureCode = "invalid_request"
	ClientFailureModelUnavailable  ClientFailureCode = "model_not_found"
	ClientFailureNoRoute           ClientFailureCode = "no_route"
	MaxClientFailureMessageBytes                     = 4096
)

// ClientFailure is public, bounded presentation metadata, not provider evidence
// or permission to retry. Values are copied across event/transform boundaries.
type ClientFailure struct {
	Code    ClientFailureCode `json:"code"`
	Type    string            `json:"type"`
	Message string            `json:"message"`
}

func (f ClientFailure) Valid() bool {
	return validFailureIdentifier(string(f.Code), 128) && validFailureIdentifier(f.Type, 64) && f.Message != "" && len(f.Message) <= MaxClientFailureMessageBytes && utf8.ValidString(f.Message)
}

func validFailureIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == ':' {
			continue
		}
		return false
	}
	return true
}

type clientFailureError struct {
	detail ClientFailure
	cause  error
}

func (e *clientFailureError) Error() string                { return e.detail.Message }
func (e *clientFailureError) Unwrap() error                { return e.cause }
func (e *clientFailureError) ClientFailure() ClientFailure { return e.detail }

// NewClientFailureError attaches explicit safe metadata without changing error
// identity, upstream effect, credential recovery or replay policy.
func NewClientFailureError(detail ClientFailure, cause error) error {
	if !detail.Valid() {
		return errors.New("invalid client failure descriptor")
	}
	return &clientFailureError{detail: detail, cause: cause}
}

func ClientFailureForError(err error) ClientFailure {
	if err == nil {
		return ClientFailure{}
	}
	var carrier interface{ ClientFailure() ClientFailure }
	if errors.As(err, &carrier) {
		if detail := carrier.ClientFailure(); detail.Valid() {
			return detail
		}
	}
	if errors.Is(err, ErrContinuityRestartRequired) {
		return ClientFailure{Code: ClientFailureContinuityRestart, Type: "invalid_request_error", Message: ErrContinuityRestartRequired.Error()}
	}
	code, typ, message := ClientFailureUpstream, "upstream_error", err.Error()
	var input *operations.InputError
	switch {
	case errors.Is(err, ErrModelNotPublished):
		code, typ, message = ClientFailureModelUnavailable, "invalid_request_error", ErrModelNotPublished.Error()
	case errors.Is(err, ErrNoRoute):
		code, message = ClientFailureNoRoute, ErrNoRoute.Error()
	case errors.Is(err, ErrOperationRequired):
		code, typ, message = ClientFailureInvalidRequest, "invalid_request_error", ErrOperationRequired.Error()
	case errors.As(err, &input):
		code, typ, message = ClientFailureInvalidRequest, "invalid_request_error", input.Error()
	}
	if message == "" || len(message) > MaxClientFailureMessageBytes || !utf8.ValidString(message) {
		message = "upstream request failed"
	}
	return ClientFailure{Code: code, Type: typ, Message: message}
}

func withOutcomeClientFailure(outcome ClassifiedOutcome, cause error) error {
	if outcome.ClientFailure == (ClientFailure{}) {
		return cause
	}
	if !outcome.ClientFailure.Valid() {
		return WrapResponseFailure(ResponseFailureUnknown, errors.New("invalid classified client failure"))
	}
	return NewClientFailureError(outcome.ClientFailure, cause)
}
