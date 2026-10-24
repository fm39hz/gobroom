package ingress

import (
	"context"
	"encoding/json"
	"mime"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func validFailureContext() ClientFailureContext {
	return ClientFailureContext{
		Failure:      kernel.ClientFailure{Code: kernel.ClientFailureInvalidRequest, Type: "invalid_request_error", Message: "safe message"},
		OperationRef: extensions.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 1},
		RequestID:    "req-123", Model: "model-1", Status: 400,
		Effect: kernel.EffectNotDispatched, Retry: kernel.RetryNever,
	}
}

func TestClientFailureContextValidityAndCloneOwnsDiagnostics(t *testing.T) {
	ctx := validFailureContext()
	ctx.Diagnostics.Paths = []string{"/operation/prompt"}
	ctx.Diagnostics.Reasons = []string{"required_field"}
	if !ctx.Valid() {
		t.Fatal("valid failure context was rejected")
	}

	clone := ctx.Clone()
	clone.Diagnostics.Paths[0] = "/changed"
	clone.Diagnostics.Reasons[0] = "changed"
	if ctx.Diagnostics.Paths[0] != "/operation/prompt" || ctx.Diagnostics.Reasons[0] != "required_field" {
		t.Fatal("clone retained mutable diagnostic slices")
	}

	invalid := validFailureContext()
	invalid.OperationRef.ID = ""
	if invalid.Valid() {
		t.Fatal("context without exact operation ref was accepted")
	}
	invalid = validFailureContext()
	invalid.RequestID = strings.Repeat("x", 129)
	if invalid.Valid() {
		t.Fatal("oversized request ID was accepted")
	}
	invalid = validFailureContext()
	invalid.Diagnostics.Paths = []string{"/bad\npath"}
	if invalid.Valid() {
		t.Fatal("diagnostic control character was accepted")
	}
	invalid = validFailureContext()
	invalid.Diagnostics.Paths = make([]string, 33)
	for i := range invalid.Diagnostics.Paths {
		invalid.Diagnostics.Paths[i] = "/field"
	}
	if invalid.Valid() {
		t.Fatal("too many diagnostic paths were accepted")
	}
}

func TestFailureContextPreservesValidMultilinePublicMessage(t *testing.T) {
	input := validFailureContext()
	input.Failure.Message = "First public line\nSecond public line"
	if !input.Failure.Valid() {
		t.Fatal("fixture must satisfy shared public failure contract")
	}
	if !input.Valid() {
		t.Fatal("wire context rejected valid bounded public message")
	}
	encoded, err := (JSONFailureEncoder{}).RenderFailure(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(encoded.Body), &response); err != nil || response.Error.Message != input.Failure.Message {
		t.Fatalf("valid message changed: %s / %v", encoded.Body, err)
	}
}

func TestFailureResponseRejectsDispositionTokenWithoutMediaSubtype(t *testing.T) {
	if (ClientFailureResponse{ContentType: "attachment", Body: `{}`}).Valid() {
		t.Fatal("Content-Disposition token is not a complete Content-Type")
	}
}

func TestClientFailureContextExactEncodedBudget(t *testing.T) {
	input := validFailureContext()
	input.OperationRef.ID = ""
	base, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	input.OperationRef.ID = strings.Repeat("x", 32768-len(base))
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) != 32768 || !input.Valid() {
		t.Fatalf("exact context budget rejected: bytes=%d err=%v", len(encoded), err)
	}
	input.OperationRef.ID += "x"
	if input.Valid() {
		t.Fatal("32769-byte context accepted")
	}
}

func TestAnthropicFailureUsesNativeUpstreamErrorType(t *testing.T) {
	input := validFailureContext()
	input.Status = 502
	input.Failure = kernel.ClientFailure{Code: kernel.ClientFailureUpstream, Type: "upstream_error", Message: "safe upstream failure"}
	response, err := (AnthropicFailureEncoder{}).RenderFailure(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(response.Body), &body); err != nil || body.Error.Type != "api_error" {
		t.Fatalf("not Anthropic failure wire: %s / %v", response.Body, err)
	}
}

func TestClientFailureResponseValidity(t *testing.T) {
	valid := ClientFailureResponse{ContentType: "application/json; charset=utf-8", Body: "{}"}
	if !valid.Valid() {
		t.Fatal("valid response rejected")
	}
	if _, _, err := mime.ParseMediaType(valid.ContentType); err != nil {
		t.Fatalf("stock content type is not a media type: %v", err)
	}

	for _, response := range []ClientFailureResponse{
		{ContentType: "not a media type", Body: "{}"},
		{ContentType: "application/json\r\nX-Injected: yes", Body: "{}"},
		{ContentType: "application/json", Body: strings.Repeat("x", 65537)},
	} {
		if response.Valid() {
			t.Errorf("invalid response accepted: content-type bytes=%d body bytes=%d", len(response.ContentType), len(response.Body))
		}
	}
	if (ClientFailureResponse{ContentType: "application/octet-stream", Body: string([]byte{0xff, 0x00, 0x80})}).Valid() == false {
		t.Fatal("opaque binary body must not be required to be UTF-8")
	}
}

func TestStockFailureEncodersProduceDecodedDialectEnvelopes(t *testing.T) {
	ctx := validFailureContext()
	ctx.Diagnostics.Paths = []string{"/operation/prompt"}
	ctx.Diagnostics.Reasons = []string{"required_field"}

	t.Run("json", func(t *testing.T) {
		response, err := (JSONFailureEncoder{}).RenderFailure(t.Context(), ctx)
		if err != nil || !response.Valid() {
			t.Fatalf("render failure response=%#v err=%v", response, err)
		}
		var envelope struct {
			Error struct {
				Code    string   `json:"code"`
				Type    string   `json:"type"`
				Message string   `json:"message"`
				Model   string   `json:"model"`
				Paths   []string `json:"paths"`
				Reasons []string `json:"reasons"`
				Effect  string   `json:"effect"`
				Retry   string   `json:"retry"`
			} `json:"error"`
			Operation string `json:"operation"`
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
			t.Fatalf("stock JSON body did not decode: %v", err)
		}
		if envelope.Error.Code != string(ctx.Failure.Code) || envelope.Error.Type != ctx.Failure.Type || envelope.Error.Message != ctx.Failure.Message || envelope.Error.Model != ctx.Model || envelope.Error.Paths[0] != "/operation/prompt" || envelope.Error.Reasons[0] != "required_field" || envelope.Error.Effect != string(ctx.Effect) || envelope.Error.Retry != string(ctx.Retry) || envelope.Operation != ctx.OperationRef.Key() || envelope.RequestID != ctx.RequestID {
			t.Fatalf("decoded JSON failure envelope lost safe context: %#v", envelope)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		response, err := (AnthropicFailureEncoder{}).RenderFailure(t.Context(), ctx)
		if err != nil || !response.Valid() {
			t.Fatalf("render failure response=%#v err=%v", response, err)
		}
		var envelope struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(response.Body), &envelope); err != nil {
			t.Fatalf("Anthropic body did not decode: %v", err)
		}
		if envelope.Type != "error" || envelope.Error.Type != ctx.Failure.Type || envelope.Error.Message != ctx.Failure.Message {
			t.Fatalf("decoded Anthropic failure envelope mismatch: %#v", envelope)
		}
	})
}

type recordingFailureEncoder struct {
	got      ClientFailureContext
	response ClientFailureResponse
}

func (e *recordingFailureEncoder) RenderFailure(_ context.Context, ctx ClientFailureContext) (ClientFailureResponse, error) {
	e.got = ctx.Clone()
	return e.response, nil
}

func TestReusableIngressDelegatesConfiguredFailureEncoder(t *testing.T) {
	want := validFailureContext()
	want.Diagnostics.Paths = []string{"/operation/prompt"}
	want.Diagnostics.Reasons = []string{"required_field"}
	for name, invoke := range map[string]func(FailureEncoder) (ClientFailureResponse, error){
		"json ingress": func(encoder FailureEncoder) (ClientFailureResponse, error) {
			return (JSONOperationIngress{FailureEncoding: encoder}).RenderFailure(t.Context(), want)
		},
		"multipart ingress": func(encoder FailureEncoder) (ClientFailureResponse, error) {
			return (MultipartOperationIngress{FailureEncoding: encoder}).RenderFailure(t.Context(), want)
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoder := &recordingFailureEncoder{response: ClientFailureResponse{ContentType: "application/json", Body: `{"custom":true}`}}
			got, err := invoke(encoder)
			if err != nil || got != encoder.response {
				t.Fatalf("delegated response=%#v err=%v", got, err)
			}
			if encoder.got.Failure != want.Failure || encoder.got.OperationRef != want.OperationRef || encoder.got.RequestID != want.RequestID || encoder.got.Model != want.Model || encoder.got.Status != want.Status || encoder.got.Effect != want.Effect || encoder.got.Retry != want.Retry || encoder.got.Diagnostics.Paths[0] != want.Diagnostics.Paths[0] {
				t.Fatalf("encoder received altered context: %#v", encoder.got)
			}
		})
	}
}
