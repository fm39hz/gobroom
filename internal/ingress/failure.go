package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

const (
	maxFailureContextBytes    = 32 << 10
	maxFailureResponseBytes   = 64 << 10
	maxFailureContentTypeSize = 256
	maxFailureSafeTextBytes   = 128
)

// ClientFailureContext contains only host-selected, bounded presentation data.
// Its diagnostics slices are owned by the context value and cloned at module
// boundaries.
type ClientFailureContext struct {
	Failure      kernel.ClientFailure            `json:"failure"`
	Diagnostics  kernel.ClientFailureDiagnostics `json:"diagnostics"`
	OperationRef extensions.Ref                  `json:"operationRef"`
	RequestID    string                          `json:"requestId,omitempty"`
	Model        string                          `json:"model,omitempty"`
	Status       int                             `json:"status"`
	Effect       kernel.UpstreamEffect           `json:"effect"`
	Retry        kernel.RetryAction              `json:"retry"`
}

// Valid checks field invariants and the full encoded-size budget without
// mutating or retaining caller-provided buffers.
func (c ClientFailureContext) Valid() bool {
	if !c.Failure.Valid() || c.OperationRef.Kind != "operation" || c.OperationRef.Validate() != nil {
		return false
	}
	if len(c.OperationRef.Kind)+len(c.OperationRef.ID) > maxFailureContextBytes || !validFailureText(c.OperationRef.Kind) || !validFailureText(c.OperationRef.ID) {
		return false
	}
	if c.Status < 400 || c.Status > 599 || !validFailureSafeText(c.RequestID) || !validFailureSafeText(c.Model) {
		return false
	}
	switch c.Effect {
	case kernel.EffectUnknown, kernel.EffectNotDispatched, kernel.EffectRejected, kernel.EffectAccepted:
	default:
		return false
	}
	switch c.Retry {
	case kernel.RetryNow, kernel.RetryAfter, kernel.RetryNever, kernel.RetryUnknown:
	default:
		return false
	}
	if !c.Diagnostics.Valid() {
		return false
	}
	encoded, err := json.Marshal(c)
	return err == nil && len(encoded) <= maxFailureContextBytes
}

// Clone returns a context whose mutable diagnostic slices are independently
// owned.
func (c ClientFailureContext) Clone() ClientFailureContext {
	c.Diagnostics = c.Diagnostics.Clone()
	return c
}

func validFailureSafeText(value string) bool {
	return len(value) <= maxFailureSafeTextBytes && validFailureText(value)
}

func validFailureText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	return !hasControl(value)
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ClientFailureResponse contains only an explicit media type and immutable
// wire body. The body may contain arbitrary bytes encoded in a Go string.
type ClientFailureResponse struct {
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

func (r ClientFailureResponse) Valid() bool {
	if len(r.ContentType) == 0 || len(r.ContentType) > maxFailureContentTypeSize || !utf8.ValidString(r.ContentType) || len(r.Body) > maxFailureResponseBytes {
		return false
	}
	for _, char := range r.ContentType {
		if unicode.IsControl(char) {
			return false
		}
	}
	mediaType, _, err := mime.ParseMediaType(r.ContentType)
	if err != nil {
		return false
	}
	typeName, subtype, found := strings.Cut(mediaType, "/")
	return found && typeName != "" && subtype != ""
}

// FailureEncoder renders one pre-commit client failure in a client dialect.
type FailureEncoder interface {
	RenderFailure(context.Context, ClientFailureContext) (ClientFailureResponse, error)
}

// JSONFailureEncoder renders the shared generic JSON failure envelope.
type JSONFailureEncoder struct{}

func (JSONFailureEncoder) RenderFailure(_ context.Context, failure ClientFailureContext) (ClientFailureResponse, error) {
	if !failure.Valid() {
		return ClientFailureResponse{}, errors.New("invalid client failure context")
	}
	failure = failure.Clone()
	var body struct {
		Error struct {
			Code    kernel.ClientFailureCode `json:"code"`
			Type    string                   `json:"type"`
			Message string                   `json:"message"`
			Model   string                   `json:"model,omitempty"`
			Paths   []string                 `json:"paths,omitempty"`
			Reasons []string                 `json:"reasons,omitempty"`
			Effect  kernel.UpstreamEffect    `json:"effect"`
			Retry   kernel.RetryAction       `json:"retry"`
		} `json:"error"`
		Operation string `json:"operation"`
		RequestID string `json:"requestId,omitempty"`
	}
	body.Error.Code = failure.Failure.Code
	body.Error.Type = failure.Failure.Type
	body.Error.Message = failure.Failure.Message
	body.Error.Model = failure.Model
	body.Error.Paths = append([]string(nil), failure.Diagnostics.Paths...)
	body.Error.Reasons = append([]string(nil), failure.Diagnostics.Reasons...)
	body.Error.Effect = failure.Effect
	body.Error.Retry = failure.Retry
	body.Operation = failure.OperationRef.Key()
	body.RequestID = failure.RequestID
	encoded, err := json.Marshal(body)
	if err != nil {
		return ClientFailureResponse{}, errors.New("encode client failure response")
	}
	response := ClientFailureResponse{ContentType: "application/json", Body: string(encoded)}
	if !response.Valid() {
		return ClientFailureResponse{}, errors.New("invalid encoded client failure response")
	}
	return response, nil
}

// AnthropicFailureEncoder renders the Anthropic Messages error envelope.
type AnthropicFailureEncoder struct{}

func (AnthropicFailureEncoder) RenderFailure(_ context.Context, failure ClientFailureContext) (ClientFailureResponse, error) {
	if !failure.Valid() {
		return ClientFailureResponse{}, errors.New("invalid client failure context")
	}
	failure = failure.Clone()
	body := struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id,omitempty"`
	}{Type: "error"}
	body.Error.Type = anthropicFailureType(failure.Status)
	body.Error.Message = failure.Failure.Message
	body.RequestID = failure.RequestID
	encoded, err := json.Marshal(body)
	if err != nil {
		return ClientFailureResponse{}, errors.New("encode client failure response")
	}
	response := ClientFailureResponse{ContentType: "application/json", Body: string(encoded)}
	if !response.Valid() {
		return ClientFailureResponse{}, errors.New("invalid encoded client failure response")
	}
	return response, nil
}

func anthropicFailureType(status int) string {
	switch status {
	case 401:
		return "authentication_error"
	case 402:
		return "billing_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 409:
		return "conflict_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 504:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func renderWithFailureEncoder(ctx context.Context, encoder FailureEncoder, failure ClientFailureContext) (ClientFailureResponse, error) {
	if !failure.Valid() {
		return ClientFailureResponse{}, errors.New("invalid client failure context")
	}
	if encoder == nil {
		encoder = JSONFailureEncoder{}
	}
	response, err := encoder.RenderFailure(ctx, failure.Clone())
	if err != nil {
		return ClientFailureResponse{}, err
	}
	if !response.Valid() {
		return ClientFailureResponse{}, errors.New("invalid client failure response")
	}
	return response, nil
}
