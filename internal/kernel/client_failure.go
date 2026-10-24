package kernel

import (
	"encoding/json"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/fm39hz/gobroom/internal/operations"
)

type ClientFailureCode string

const (
	ClientFailureUpstream                 ClientFailureCode = "upstream_error"
	ClientFailureContinuityRestart        ClientFailureCode = "continuity_restart_required"
	ClientFailureInvalidRequest           ClientFailureCode = "invalid_request"
	ClientFailureModelUnavailable         ClientFailureCode = "model_not_found"
	ClientFailureNoRoute                  ClientFailureCode = "no_route"
	MaxClientFailureMessageBytes                            = 4096
	maxClientFailureDiagnosticItems                         = 32
	maxClientFailureDiagnosticPathBytes                     = 256
	maxClientFailureDiagnosticReasonBytes                   = 128
	maxClientFailureDiagnosticsBytes                        = 8192
)

const (
	defaultClientFailureMessage = "upstream request failed"
	invalidClientFailureMessage = "invalid request"
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

// ClientFailureDiagnostics contains bounded, public semantic input locations
// and reason codes. It must not contain raw values or error text.
type ClientFailureDiagnostics struct {
	Paths   []string `json:"paths,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

func (d ClientFailureDiagnostics) Clone() ClientFailureDiagnostics {
	return ClientFailureDiagnostics{
		Paths:   append([]string(nil), d.Paths...),
		Reasons: append([]string(nil), d.Reasons...),
	}
}

func (d ClientFailureDiagnostics) Valid() bool {
	if len(d.Paths) > maxClientFailureDiagnosticItems || len(d.Reasons) > maxClientFailureDiagnosticItems {
		return false
	}
	for _, path := range d.Paths {
		if len(path) == 0 || len(path) > maxClientFailureDiagnosticPathBytes || !utf8.ValidString(path) || path[0] != '/' {
			return false
		}
		for _, r := range path {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	for _, reason := range d.Reasons {
		if !validFailureIdentifier(reason, maxClientFailureDiagnosticReasonBytes) {
			return false
		}
	}
	encoded, err := json.Marshal(d)
	return err == nil && len(encoded) <= maxClientFailureDiagnosticsBytes
}

// ClientFailureDiagnosticsForError extracts only diagnostics explicitly
// supplied by an error carrier. Invalid optional metadata is omitted.
func ClientFailureDiagnosticsForError(err error) ClientFailureDiagnostics {
	if err == nil {
		return ClientFailureDiagnostics{}
	}
	var carrier interface {
		ClientFailureDiagnostics() ClientFailureDiagnostics
	}
	if !errors.As(err, &carrier) {
		return ClientFailureDiagnostics{}
	}
	diagnostics := carrier.ClientFailureDiagnostics()
	if !diagnostics.Valid() {
		return ClientFailureDiagnostics{}
	}
	return diagnostics.Clone()
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
	return ClientFailureForErrorWithFallback(err, ClientFailure{
		Code: ClientFailureUpstream, Type: "upstream_error", Message: defaultClientFailureMessage,
	})
}

// ClientFailureForErrorWithFallback projects an error into public metadata.
// The fallback is used only for unknown errors; recognized safe carriers and
// kernel classifications take precedence.
func ClientFailureForErrorWithFallback(err error, fallback ClientFailure) ClientFailure {
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
	var input *operations.InputError
	switch {
	case errors.Is(err, ErrModelNotPublished):
		return ClientFailure{Code: ClientFailureModelUnavailable, Type: "invalid_request_error", Message: ErrModelNotPublished.Error()}
	case errors.Is(err, ErrNoRoute):
		return ClientFailure{Code: ClientFailureNoRoute, Type: "upstream_error", Message: ErrNoRoute.Error()}
	case errors.Is(err, ErrOperationRequired):
		return ClientFailure{Code: ClientFailureInvalidRequest, Type: "invalid_request_error", Message: ErrOperationRequired.Error()}
	case errors.As(err, &input):
		return ClientFailure{Code: ClientFailureInvalidRequest, Type: "invalid_request_error", Message: invalidClientFailureMessage}
	}
	if !fallback.Valid() {
		return ClientFailure{Code: ClientFailureUpstream, Type: "upstream_error", Message: defaultClientFailureMessage}
	}
	return fallback
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
