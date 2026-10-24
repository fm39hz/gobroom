package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/ingress"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
	"github.com/fm39hz/gobroom/internal/operations"
	"github.com/fm39hz/gobroom/internal/store"
)

type boundaryFailureIngress struct {
	JSONOperationIngress
	decodeError      error
	encodeMode       string
	decodes, renders int
}

func (c *boundaryFailureIngress) Decode(*http.Request) (normalize.Request, error) {
	c.decodes++
	if c.decodeError != nil {
		return normalize.Request{}, c.decodeError
	}
	return normalize.Request{Model: "role", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}, nil
}

func (c *boundaryFailureIngress) RenderFailure(_ context.Context, input ingress.ClientFailureContext) (ingress.ClientFailureResponse, error) {
	c.renders++
	switch c.encodeMode {
	case "error":
		return ingress.ClientFailureResponse{}, errors.New("private-encoder-error-947e")
	case "oversize":
		return ingress.ClientFailureResponse{ContentType: "application/json", Body: strings.Repeat("x", 65537)}, nil
	case "header":
		return ingress.ClientFailureResponse{ContentType: "application/json\r\nX-Leak: secret", Body: `{}`}, nil
	case "media":
		return ingress.ClientFailureResponse{ContentType: "invalid media type", Body: `{}`}, nil
	}
	data, err := json.Marshal(struct {
		Kind        string                   `json:"kind"`
		Code        kernel.ClientFailureCode `json:"why"`
		HTTP        int                      `json:"http"`
		Effect      kernel.UpstreamEffect    `json:"effect"`
		Retry       kernel.RetryAction       `json:"retry"`
		Correlation string                   `json:"correlation"`
		Operation   string                   `json:"operation"`
	}{"client-specific-failure", input.Failure.Code, input.Status, input.Effect, input.Retry, input.RequestID, input.OperationRef.ID})
	return ingress.ClientFailureResponse{ContentType: "application/vnd.fixture-error+json", Body: string(data)}, err
}

func failureBoundaryServer(t *testing.T, codec *boundaryFailureIngress) *Server {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	server := NewServer(s)
	codec.JSONOperationIngress = JSONOperationIngress{CodecID: "fixture.failure-boundary", IngressContractVersion: 1, Operation: string(normalize.OperationChatGenerate), OperationContractVersion: 1, Endpoints: []IngressRoute{{Method: http.MethodPost, Path: "/v1/client-failure"}}}
	if err := server.RegisterOperationIngress(codec); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestRegisteredClientOwnsPrecommitFailureWireAndHostAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		auth    bool
		decode  error
		execute error
		status  int
		effect  kernel.UpstreamEffect
		calls   int
	}{
		{"auth", true, nil, nil, 401, kernel.EffectNotDispatched, 0},
		{"decode", false, errors.New("private-decode-947e"), nil, 400, kernel.EffectNotDispatched, 0},
		{"input schema", false, nil, &operations.InputError{Err: errors.New("private-schema-947e")}, 400, kernel.EffectNotDispatched, 1},
		{"no route", false, nil, kernel.ErrNoRoute, 503, kernel.EffectUnknown, 1},
		{"ordinary execute", false, nil, errors.New("private-executor-947e"), 502, kernel.EffectUnknown, 1},
		{"accepted pre-byte", false, nil, &kernel.ReplaySuppressedError{Effect: kernel.EffectAccepted, Cause: errors.New("private-provider-947e")}, 502, kernel.EffectAccepted, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := &boundaryFailureIngress{decodeError: tc.decode}
			server := failureBoundaryServer(t, codec)
			if tc.auth {
				server.SetDataPlaneToken("expected-client-key")
			}
			calls := 0
			server.SetExecutor(func(context.Context, normalize.Request, http.ResponseWriter) error { calls++; return tc.execute })
			request := httptest.NewRequest(http.MethodPost, "/v1/client-failure", strings.NewReader(`{}`))
			request.Header.Set("X-Request-ID", "failure-case-42")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			var body struct {
				Kind        string                `json:"kind"`
				HTTP        int                   `json:"http"`
				Effect      kernel.UpstreamEffect `json:"effect"`
				Retry       kernel.RetryAction    `json:"retry"`
				Correlation string                `json:"correlation"`
				Operation   string                `json:"operation"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != tc.status || body.Kind != "client-specific-failure" || body.HTTP != tc.status || body.Effect != tc.effect || body.Retry != kernel.RetryNever || body.Correlation != "failure-case-42" || body.Operation != string(normalize.OperationChatGenerate) || codec.renders != 1 || calls != tc.calls {
				t.Fatalf("wrong client wire/host facts: status=%d body=%s renders=%d dispatches=%d", response.Code, response.Body.String(), codec.renders, calls)
			}
			if tc.auth && codec.decodes != 0 {
				t.Fatal("unauthorized request reached decoder")
			}
			if strings.Contains(response.Body.String(), "private-") {
				t.Fatal("private cause crossed failure renderer input")
			}
		})
	}
}

func TestExecutionFailureEffectPreservesReplaySuppressionAuthority(t *testing.T) {
	for _, effect := range []kernel.UpstreamEffect{kernel.EffectUnknown, kernel.EffectAccepted, kernel.EffectRejected, kernel.EffectNotDispatched} {
		err := &kernel.ReplaySuppressedError{Effect: effect, Cause: &operations.InputError{Err: errors.New("private-input")}}
		if got := executionFailureEffect(err); got != effect {
			t.Fatalf("effect = %q, want authoritative %q", got, effect)
		}
	}
}

func TestBadClientFailureEncoderFailsClosedBeforeWriting(t *testing.T) {
	for _, mode := range []string{"error", "oversize", "header", "media"} {
		t.Run(mode, func(t *testing.T) {
			codec := &boundaryFailureIngress{decodeError: errors.New("private-decode-947e"), encodeMode: mode}
			server := failureBoundaryServer(t, codec)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/client-failure", strings.NewReader(`{}`)))
			if response.Code != 500 || response.Body.Len() != 0 || codec.renders != 1 || response.Header().Get("X-Leak") != "" {
				t.Fatalf("unsafe fallback/partial write: %d %s renders=%d", response.Code, response.Body.String(), codec.renders)
			}
		})
	}
}

func TestPrecommitFailureRestoresHostHeaders(t *testing.T) {
	for _, mode := range []string{"", "error"} {
		t.Run("encoder-"+mode, func(t *testing.T) {
			codec := &boundaryFailureIngress{encodeMode: mode}
			server := failureBoundaryServer(t, codec)
			server.SetExecutor(func(_ context.Context, _ normalize.Request, w http.ResponseWriter) error {
				w.Header().Set("X-Private-Upstream", "private-secret")
				w.Header().Set("Set-Cookie", "private-secret")
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Length", "99999")
				w.Header().Set("X-Request-ID", "private-upstream-id")
				return errors.New("private-decode-failure")
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/client-failure", strings.NewReader(`{}`))
			request.Header.Set("X-Request-ID", "host-request-42")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			for _, name := range []string{"X-Private-Upstream", "Set-Cookie", "Content-Encoding", "Content-Length"} {
				if response.Header().Get(name) != "" {
					t.Errorf("staged upstream %s survived: %v", name, response.Header())
				}
			}
			if response.Header().Get("X-Request-ID") != "host-request-42" || strings.Contains(response.Body.String(), "private-") {
				t.Fatalf("host correlation replaced/private data leaked: headers=%v body=%s", response.Header(), response.Body.String())
			}
			if mode == "error" && (response.Code != 500 || response.Body.Len() != 0) {
				t.Fatalf("invalid encoder did not fail closed: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHostFailureMetadataBoundsAndContextOverflow(t *testing.T) {
	for _, model := range []string{"", strings.Repeat("x", 128), strings.Repeat("x", 129), "bad\nmodel", string([]byte{0xff})} {
		codec := JSONOperationIngress{CodecID: "fixture", IngressContractVersion: 1, Operation: "chat.generate", OperationContractVersion: 1}
		response := httptest.NewRecorder()
		server := &Server{}
		server.renderClientFailure(response, httptest.NewRequest(http.MethodPost, "/", nil), codec, model, 400, kernel.ClientFailure{Code: "invalid_request", Type: "invalid_request_error", Message: "safe"}, nil, kernel.EffectNotDispatched)
		var wire struct {
			Error struct {
				Model string `json:"model"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || response.Code != 400 {
			t.Fatalf("metadata rendering: status=%d err=%v", response.Code, err)
		}
		want := model
		if len(model) > 128 || model == "bad\nmodel" || model == string([]byte{0xff}) {
			want = ""
		}
		if wire.Error.Model != want {
			t.Fatalf("model was truncated or unsafe: got=%q want=%q", wire.Error.Model, want)
		}
	}
	codec := &boundaryFailureIngress{JSONOperationIngress: JSONOperationIngress{Operation: strings.Repeat("x", 32768), OperationContractVersion: 1}}
	response := httptest.NewRecorder()
	(&Server{}).renderClientFailure(response, httptest.NewRequest(http.MethodPost, "/", nil), codec, "", 400, kernel.ClientFailure{Code: "invalid_request", Type: "invalid_request_error", Message: "safe"}, nil, kernel.EffectNotDispatched)
	if response.Code != 500 || response.Body.Len() != 0 || codec.renders != 0 {
		t.Fatalf("oversized context reached encoder: status=%d bytes=%d calls=%d", response.Code, response.Body.Len(), codec.renders)
	}
}

func TestCommittedExecutionNeverInvokesPrecommitFailureRenderer(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		codec := &boundaryFailureIngress{}
		server := failureBoundaryServer(t, codec)
		server.SetExecutor(func(_ context.Context, _ normalize.Request, w http.ResponseWriter) error {
			_, _ = io.WriteString(w, "committed-prefix")
			return errors.New("private-after-commit")
		})
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/client-failure", strings.NewReader(`{}`)))
		if response.Code != 200 || response.Body.String() != "committed-prefix" || codec.renders != 0 {
			t.Fatalf("second body aftercommit: %d %s renders=%d", response.Code, response.Body.String(), codec.renders)
		}
	})
	for _, mode := range []string{"headers", "flush"} {
		t.Run(mode, func(t *testing.T) {
			codec := &boundaryFailureIngress{}
			server := failureBoundaryServer(t, codec)
			server.SetExecutor(func(_ context.Context, _ normalize.Request, w http.ResponseWriter) error {
				if mode == "headers" {
					w.WriteHeader(http.StatusAccepted)
				} else if err := http.NewResponseController(w).Flush(); err != nil {
					t.Fatal(err)
				}
				return errors.New("private-after-commit")
			})
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/client-failure", strings.NewReader(`{}`)))
			want := http.StatusOK
			if mode == "headers" {
				want = http.StatusAccepted
			}
			if response.Code != want || response.Body.Len() != 0 || codec.renders != 0 {
				t.Fatalf("failure rendered after %s commitment: status=%d body=%q renders=%d", mode, response.Code, response.Body.String(), codec.renders)
			}
		})
	}
}
