package api

import (
	"errors"
	"io"
	"net/http"
	"unicode"
	"unicode/utf8"

	"github.com/fm39hz/gobroom/internal/ingress"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/operations"
)

func boundedFailureValue(value string) string {
	if len(value) > 128 || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return value
}

// renderClientFailure passes presentation data only. The codec never receives
// the Go error, request/headers, writer, lease or provider continuation state.
func (s *Server) renderClientFailure(w http.ResponseWriter, r *http.Request, codec OperationIngress, model string, status int, fallback kernel.ClientFailure, cause error, effect kernel.UpstreamEffect) {
	failure := fallback
	if cause != nil {
		failure = kernel.ClientFailureForErrorWithFallback(cause, fallback)
	}
	input := ingress.ClientFailureContext{
		Failure: failure, Diagnostics: kernel.ClientFailureDiagnosticsForError(cause),
		OperationRef: codec.OperationRef(), RequestID: boundedFailureValue(w.Header().Get("X-Request-ID")),
		Model: boundedFailureValue(model), Status: status, Effect: effect, Retry: kernel.RetryNever,
	}
	if !input.Valid() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	encoded, err := codec.RenderFailure(r.Context(), input.Clone())
	if err != nil || !encoded.Valid() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", encoded.ContentType)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, encoded.Body)
}

func executionFailureStatus(err error) int {
	var input *operations.InputError
	switch {
	case errors.As(err, &input):
		return http.StatusBadRequest
	case errors.Is(err, kernel.ErrModelNotPublished):
		return http.StatusNotFound
	case errors.Is(err, kernel.ErrNoRoute):
		return http.StatusServiceUnavailable
	case errors.Is(err, kernel.ErrContinuityRestartRequired):
		return http.StatusConflict
	default:
		return http.StatusBadGateway
	}
}

func executionFailureEffect(err error) kernel.UpstreamEffect {
	var suppressed *kernel.ReplaySuppressedError
	if errors.As(err, &suppressed) {
		switch suppressed.Effect {
		case kernel.EffectNotDispatched, kernel.EffectRejected, kernel.EffectAccepted:
			return suppressed.Effect
		}
		return kernel.EffectUnknown
	}
	// InputError is emitted by operation preparation, before route dispatch.
	// A replay-suppression wrapper above remains authoritative even when its
	// cause happens to contain input validation metadata.
	var input *operations.InputError
	if errors.As(err, &input) {
		return kernel.EffectNotDispatched
	}
	return kernel.EffectUnknown
}
