package externalprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
)

// Qoder framing is source-specific; the registered OpenAI decoder owns all
// text, tool, usage and completion semantics. No outer frame reaches emit.
type qoderEnvelopeDecoder struct{ delegate provider.ResponseDecoder }

func (qoderEnvelopeDecoder) ID() string { return "example.qoder-native-envelope" }
func (d qoderEnvelopeDecoder) PossibleEvents() []provider.ResponseEventKind {
	return d.delegate.PossibleEvents()
}
func (d qoderEnvelopeDecoder) ClassifyError(status int, body []byte) provider.ErrorClass {
	return d.delegate.ClassifyError(status, body)
}

func (d qoderEnvelopeDecoder) Decode(ctx context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, hooks provider.StreamHooks) error {
	if response.Body == nil {
		return errors.New("Qoder response body is required")
	}
	upstream := response.Body
	var closeOnce sync.Once
	closeUpstream := func() { closeOnce.Do(func() { _ = upstream.Close() }) }
	// Cancellation must release a blocked transport Read. There is no framing
	// goroutine: AfterFunc runs only the close callback when canceled. Once also
	// joins an in-progress close before Decode returns.
	stopCancel := context.AfterFunc(ctx, closeUpstream)
	defer func() { stopCancel(); closeUpstream() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if response.Status != http.StatusOK {
		return errors.New("Qoder upstream returned a non-200 status")
	}
	if d.delegate == nil {
		return errors.New("Qoder OpenAI response decoder is required")
	}
	scanner := bufio.NewScanner(qoderEnvelopeContextReader{ctx: ctx, reader: upstream})
	scanner.Buffer(make([]byte, 4096), 1<<20)
	reader := &qoderEnvelopeReader{ctx: ctx, scanner: scanner, closeUpstream: closeUpstream}
	response.Body = reader
	response.Headers = response.Headers.Clone()
	if response.Headers == nil {
		response.Headers = make(http.Header)
	}
	response.Headers.Set("Content-Type", "text/event-stream")
	return d.delegate.Decode(ctx, response, func(event provider.ResponseEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(event); err != nil {
			return err
		}
		return ctx.Err()
	}, hooks)
}

// Check both sides of Read so cancellation takes precedence over the transport
// error returned when the cancellation callback closes an in-flight read.
type qoderEnvelopeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r qoderEnvelopeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if cancelErr := r.ctx.Err(); cancelErr != nil {
		return 0, cancelErr
	}
	return n, err
}

// Read presents one unwrapped SSE frame at a time. In particular, a delegate
// scanner cannot cause this adapter to drain the provider's keepalive tail.
type qoderEnvelopeReader struct {
	ctx           context.Context
	scanner       *bufio.Scanner
	closeUpstream func()
	pending       []byte
	terminal      bool
	failure       error
}

func (r *qoderEnvelopeReader) Close() error { r.closeUpstream(); return nil }

func (r *qoderEnvelopeReader) fail(err error) (int, error) {
	r.failure = err
	r.closeUpstream()
	return 0, err
}

func (r *qoderEnvelopeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return r.fail(err)
	}
	if r.failure != nil {
		return 0, r.failure
	}
	for len(r.pending) == 0 {
		if r.terminal {
			return 0, io.EOF
		}
		if err := r.ctx.Err(); err != nil {
			return r.fail(err)
		}
		if !r.scanner.Scan() {
			if err := r.ctx.Err(); err != nil {
				return r.fail(err)
			}
			if err := r.scanner.Err(); err != nil {
				return r.fail(err)
			}
			// A finish_reason alone is insufficient: the outer protocol must
			// terminate, even if the semantic decoder already saw completion.
			return r.fail(io.ErrUnexpectedEOF)
		}
		if err := r.ctx.Err(); err != nil {
			return r.fail(err)
		}
		line := bytes.TrimSpace(r.scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			r.finish()
			continue
		}
		var envelope struct {
			Status json.RawMessage `json:"statusCodeValue"`
			Body   json.RawMessage `json:"body"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			return r.fail(errors.New("malformed Qoder SSE envelope"))
		}
		if len(envelope.Status) != 0 {
			var status int
			if json.Unmarshal(envelope.Status, &status) != nil || status != http.StatusOK {
				return r.fail(errors.New("Qoder envelope returned a non-200 status"))
			}
		}
		inner := bytes.TrimSpace(envelope.Body)
		if len(inner) != 0 && inner[0] == '"' {
			var body string
			if json.Unmarshal(inner, &body) != nil {
				return r.fail(errors.New("malformed Qoder string body"))
			}
			inner = bytes.TrimSpace([]byte(body))
			if bytes.Equal(inner, []byte("[DONE]")) {
				r.finish()
				continue
			}
		}
		if len(inner) == 0 || inner[0] != '{' || !json.Valid(inner) {
			return r.fail(errors.New("Qoder envelope body must contain a JSON object"))
		}
		inner, err := qoderNormalizeSourceFinish(inner)
		if err != nil {
			return r.fail(err)
		}
		// Compact whitespace so an escaped newline in a string body cannot
		// turn a valid multi-line JSON object into multiple SSE data lines.
		var compact bytes.Buffer
		if json.Compact(&compact, inner) != nil {
			return r.fail(errors.New("malformed Qoder JSON body"))
		}
		r.pending = append([]byte("data: "), compact.Bytes()...)
		r.pending = append(r.pending, '\n', '\n')
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

type qoderSourceDelta struct {
	Finish *string                    `json:"finish_reason,omitempty"`
	Extra  map[string]json.RawMessage `json:",unknown"`
}
type qoderSourceChoice struct {
	Index  int                        `json:"index"`
	Delta  qoderSourceDelta           `json:"delta"`
	Finish *string                    `json:"finish_reason,omitempty"`
	Extra  map[string]json.RawMessage `json:",unknown"`
}
type qoderSourceChunk struct {
	Choices []qoderSourceChoice        `json:"choices,omitempty"`
	Finish  *string                    `json:"finish_reason,omitempty"`
	Extra   map[string]json.RawMessage `json:",unknown"`
}

// Normalize only source-observed finish locations. All other OpenAI fields,
// including tool/usage payloads, retain their JSON binding through unknown maps.
func qoderNormalizeSourceFinish(inner []byte) ([]byte, error) {
	var chunk qoderSourceChunk
	if err := jsonv2.Unmarshal(inner, &chunk); err != nil {
		return nil, errors.New("malformed Qoder inner chunk")
	}
	changed := false
	if chunk.Finish != nil && len(chunk.Choices) == 0 {
		chunk.Choices = []qoderSourceChoice{{Index: 0, Finish: chunk.Finish}}
		changed = true
	}
	for index := range chunk.Choices {
		choice := &chunk.Choices[index]
		if choice.Finish == nil {
			if choice.Delta.Finish != nil {
				choice.Finish = choice.Delta.Finish
				changed = true
			} else if chunk.Finish != nil {
				choice.Finish = chunk.Finish
				changed = true
			}
		}
		if choice.Delta.Finish != nil {
			choice.Delta.Finish = nil
			changed = true
		}
	}
	if chunk.Finish != nil {
		chunk.Finish = nil
		changed = true
	}
	if !changed {
		return inner, nil
	}
	result, err := jsonv2.Marshal(chunk)
	if err != nil {
		return nil, errors.New("encode Qoder finish normalization failed")
	}
	return result, nil
}

func (r *qoderEnvelopeReader) finish() {
	r.terminal = true
	r.pending = []byte("data: [DONE]\n\n")
	r.closeUpstream()
}

// The main worker observed RED before this implementation was authorized.
// All semantic decoding comes from the public registry.
func qoderEnvelopeTestDecoder(t *testing.T) qoderEnvelopeDecoder {
	t.Helper()
	runtime, err := provider.NewRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := runtime.FreezeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	// NewChatCodecs registers openai-sse, not openai-chat-sse.
	_, implementation, err := catalog.Bind(provider.Ref{
		Kind: string(provider.PrimitiveResponseDecoder), ID: "openai-sse", ContractVersion: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	delegate, ok := implementation.(provider.ResponseDecoder)
	if !ok {
		t.Fatalf("public OpenAI chat primitive has type %T", implementation)
	}
	return qoderEnvelopeDecoder{delegate: delegate}
}

func qoderEnvelopeTestFrame(t *testing.T, body any, includeStatus bool) string {
	t.Helper()
	envelope := map[string]any{"body": body, "private_envelope": "QODER_PRIVATE_SENTINEL"}
	if includeStatus {
		envelope["statusCodeValue"] = 200
	}
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(wire) + "\n\n"
}

func qoderEnvelopeTestResponse(body io.ReadCloser) provider.UpstreamResponse {
	return provider.UpstreamResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: body,
	}
}

func TestQoderEnvelopePreservesSourceNestedFinishAndLateUsage(t *testing.T) {
	for _, inner := range []json.RawMessage{
		json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"partial","finish_reason":"length"}}]}`),
		json.RawMessage(`{"choices":[{"index":0,"delta":{"content":"partial"}}],"finish_reason":"length"}`),
	} {
		wire := qoderEnvelopeTestFrame(t, inner, true) + qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}`), true) + qoderEnvelopeTestFrame(t, "[DONE]", true)
		capture := &qoderEnvelopeTestCapture{}
		if err := qoderEnvelopeTestDecoder(t).Decode(t.Context(), qoderEnvelopeTestResponse(io.NopCloser(strings.NewReader(wire))), capture.emit, capture.hooks()); err != nil {
			t.Fatal(err)
		}
		finish := ""
		for _, event := range capture.events {
			if event.Kind == provider.EventContentBlockEnd {
				finish = event.StopReason
			}
		}
		if finish != "length" || len(capture.complete) != 1 || capture.complete[0].OutputTokens == nil || *capture.complete[0].OutputTokens != 3 {
			t.Fatalf("source finish/late usage lost: finish=%q completion=%#v", finish, capture.complete)
		}
	}
}

// Each Read returns at most one supplied fragment. Reading past the final
// fragment reports an error instead of hanging on an agent keepalive socket.
type qoderEnvelopeTestBody struct {
	fragments     []string
	closed        atomic.Bool
	readsAfterEnd int
	tailErr       error
}

func (b *qoderEnvelopeTestBody) Read(p []byte) (int, error) {
	if b.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(b.fragments) == 0 {
		b.readsAfterEnd++
		if b.tailErr != nil {
			return 0, b.tailErr
		}
		return 0, io.EOF
	}
	n := copy(p, b.fragments[0])
	b.fragments[0] = b.fragments[0][n:]
	if b.fragments[0] == "" {
		b.fragments = b.fragments[1:]
	}
	return n, nil
}

func (b *qoderEnvelopeTestBody) Close() error {
	b.closed.Store(true)
	return nil
}

type qoderEnvelopeTestCapture struct {
	events     []provider.ResponseEvent
	complete   []provider.UsageEvent
	firstBytes int
}

func (c *qoderEnvelopeTestCapture) emit(event provider.ResponseEvent) error {
	c.events = append(c.events, event)
	return nil
}

func (c *qoderEnvelopeTestCapture) hooks() provider.StreamHooks {
	return provider.StreamHooks{
		OnFirstByte: func(time.Time) { c.firstBytes++ },
		OnComplete:  func(usage provider.UsageEvent) { c.complete = append(c.complete, usage) },
	}
}

func (c *qoderEnvelopeTestCapture) count(kind provider.ResponseEventKind) int {
	n := 0
	for _, event := range c.events {
		if event.Kind == kind {
			n++
		}
	}
	return n
}

func qoderEnvelopeTestNoPrivateLeak(t *testing.T, capture *qoderEnvelopeTestCapture) {
	t.Helper()
	for _, event := range capture.events {
		wire, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), "QODER_PRIVATE_SENTINEL") || strings.Contains(string(event.Raw), "statusCodeValue") {
			t.Fatalf("outer envelope leaked through %s", event.Kind)
		}
	}
}

func TestQoderEnvelopeDelegatesTextToolsUsageAndFinish(t *testing.T) {
	for _, objectBody := range []bool{false, true} {
		for _, includeStatus := range []bool{false, true} {
			name := "string/default-status"
			if objectBody {
				name = "object/default-status"
			}
			if includeStatus {
				name = strings.Replace(name, "default-status", "explicit-status", 1)
			}
			t.Run(name, func(t *testing.T) {
				decoder := qoderEnvelopeTestDecoder(t)
				if decoder.ID() != "example.qoder-native-envelope" {
					t.Fatalf("decoder ID = %q", decoder.ID())
				}
				if !reflect.DeepEqual(decoder.PossibleEvents(), decoder.delegate.PossibleEvents()) {
					t.Fatal("wrapper lost delegate event capabilities")
				}
				for _, status := range []int{400, 401, 429, 500} {
					payload := []byte(`{"error":{"message":"fixture"}}`)
					if decoder.ClassifyError(status, payload) != decoder.delegate.ClassifyError(status, payload) {
						t.Fatalf("classification differs for status %d", status)
					}
				}
				chunks := []string{
					`{"id":"chat-fixture","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello "}}]}`,
					`{"choices":[{"index":0,"delta":{"content":"world","tool_calls":[{"index":2,"id":"call-7","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`,
					`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"function":{"arguments":"\"Hue\"}"}}]}}]}`,
					`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
					`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
				}
				body := &qoderEnvelopeTestBody{tailErr: errors.New("read waited for keepalive after DONE")}
				for _, chunk := range chunks {
					var value any = chunk
					if objectBody {
						value = json.RawMessage(chunk)
					}
					body.fragments = append(body.fragments, qoderEnvelopeTestFrame(t, value, includeStatus))
				}
				body.fragments = append(body.fragments, qoderEnvelopeTestFrame(t, "[DONE]", includeStatus))
				capture := &qoderEnvelopeTestCapture{}
				err := decoder.Decode(context.Background(), qoderEnvelopeTestResponse(body), func(event provider.ResponseEvent) error {
					if event.Kind == provider.EventTextDelta && len(body.fragments) == 0 {
						t.Error("text was buffered until terminal instead of streamed")
					}
					return capture.emit(event)
				}, capture.hooks())
				if err != nil {
					t.Fatal(err)
				}
				if !body.closed.Load() || body.readsAfterEnd != 0 {
					t.Fatalf("terminal cleanup: closed=%v extra reads=%d", body.closed.Load(), body.readsAfterEnd)
				}
				if capture.count(provider.EventTextDelta) != 2 || capture.count(provider.EventToolCallDelta) != 2 || capture.count(provider.EventUsage) != 1 || capture.count(provider.EventContentBlockEnd) != 1 {
					t.Fatalf("wrong semantic counts: text=%d tools=%d usage=%d finish=%d", capture.count(provider.EventTextDelta), capture.count(provider.EventToolCallDelta), capture.count(provider.EventUsage), capture.count(provider.EventContentBlockEnd))
				}
				// Existing openai-sse emits a completion for finish_reason and
				// another for DONE, with exactly one successful OnComplete hook.
				if capture.count(provider.EventResponseComplete) != 2 || len(capture.complete) != 1 || capture.firstBytes != 1 {
					t.Fatalf("completion events=%d hooks=%d first byte hooks=%d", capture.count(provider.EventResponseComplete), len(capture.complete), capture.firstBytes)
				}
				var text, arguments, toolID, toolName, stop string
				for _, event := range capture.events {
					switch event.Kind {
					case provider.EventTextDelta:
						text += event.Text
					case provider.EventToolCallDelta:
						if event.Index != 2 {
							t.Fatalf("tool index = %d", event.Index)
						}
						arguments += event.ToolArguments
						toolID += event.ToolCallID
						toolName += event.ToolName
					case provider.EventContentBlockEnd:
						stop = event.StopReason
					case provider.EventUsage:
						if event.Usage == nil || event.Usage.InputTokens == nil || *event.Usage.InputTokens != 11 || event.Usage.OutputTokens == nil || *event.Usage.OutputTokens != 7 {
							t.Fatal("usage event lost reported counters")
						}
					}
				}
				if text != "Hello world" || arguments != `{"city":"Hue"}` || toolID != "call-7" || toolName != "weather" || stop != "tool_calls" {
					t.Fatalf("text=%q arguments=%q tool=%q/%q stop=%q", text, arguments, toolID, toolName, stop)
				}
				usage := capture.complete[0]
				if usage.Status != "ok" || usage.InputTokens == nil || *usage.InputTokens != 11 || usage.OutputTokens == nil || *usage.OutputTokens != 7 {
					t.Fatal("completion lost usage")
				}
				qoderEnvelopeTestNoPrivateLeak(t, capture)
			})
		}
	}
}

func TestQoderEnvelopeDONEClosesWithoutDrainingOrInventingUsage(t *testing.T) {
	for _, terminal := range []string{"data: [DONE]\n\n", qoderEnvelopeTestFrame(t, "[DONE]", true)} {
		t.Run(terminal, func(t *testing.T) {
			body := &qoderEnvelopeTestBody{fragments: []string{terminal}, tailErr: errors.New("keepalive must not be read")}
			capture := &qoderEnvelopeTestCapture{}
			err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(body), capture.emit, capture.hooks())
			if err != nil {
				t.Fatal(err)
			}
			if !body.closed.Load() || body.readsAfterEnd != 0 {
				t.Fatalf("closed=%v extra reads=%d", body.closed.Load(), body.readsAfterEnd)
			}
			if capture.count(provider.EventResponseComplete) != 1 || capture.count(provider.EventUsage) != 0 || len(capture.complete) != 1 {
				t.Fatal("DONE must complete exactly once without a usage event")
			}
			usage := capture.complete[0]
			if usage.InputTokens != nil || usage.OutputTokens != nil || usage.EstimatedCost != nil {
				t.Fatal("absent provider usage was invented")
			}
		})
	}
}

func TestQoderEnvelopeFailuresNeverCompleteOrLeakPrivateEnvelope(t *testing.T) {
	text := qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[{"delta":{"content":"partial"}}]}`), true)
	cases := []struct {
		name, wire string
		status     int
	}{
		{"non-200-envelope", `data: {"statusCodeValue":403,"body":"QODER_PRIVATE_SENTINEL"}` + "\n\ndata: [DONE]\n\n", 200},
		{"zero-is-not-missing-status", `data: {"statusCodeValue":0,"body":"[DONE]"}` + "\n\n", 200},
		{"wrong-status-type", `data: {"statusCodeValue":"200","body":"[DONE]"}` + "\n\n", 200},
		{"malformed-outer", "data: {QODER_PRIVATE_SENTINEL\n\ndata: [DONE]\n\n", 200},
		{"malformed-inner", qoderEnvelopeTestFrame(t, "{QODER_PRIVATE_SENTINEL", true) + "data: [DONE]\n\n", 200},
		{"missing-body", "data: {\"statusCodeValue\":200}\n\ndata: [DONE]\n\n", 200},
		{"null-body", qoderEnvelopeTestFrame(t, nil, true) + "data: [DONE]\n\n", 200},
		{"scalar-body", qoderEnvelopeTestFrame(t, 17, true) + "data: [DONE]\n\n", 200},
		{"empty-stream", "", 200},
		{"truncated-envelope", text + `data: {"statusCodeValue":200,"body":"QODER_PRIVATE_SENTINEL`, 200},
		{"missing-terminal", text, 200},
		{"http-error", qoderEnvelopeTestFrame(t, "[DONE]", true), 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &qoderEnvelopeTestBody{fragments: []string{tc.wire}}
			response := qoderEnvelopeTestResponse(body)
			response.Status = tc.status
			capture := &qoderEnvelopeTestCapture{}
			err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), response, capture.emit, capture.hooks())
			if err == nil {
				t.Fatal("invalid envelope became success")
			}
			if strings.Contains(err.Error(), "QODER_PRIVATE_SENTINEL") {
				t.Fatal("private upstream payload leaked in decoder error")
			}
			if !body.closed.Load() {
				t.Fatal("failed upstream body was not closed")
			}
			if len(capture.complete) != 0 || capture.count(provider.EventResponseComplete) != 0 || capture.count(provider.EventUsage) != 0 {
				t.Fatal("failure emitted completion or fake usage")
			}
			qoderEnvelopeTestNoPrivateLeak(t, capture)
		})
	}
}

func TestQoderEnvelopeFinishWithoutDONEDoesNotReportSuccess(t *testing.T) {
	body := &qoderEnvelopeTestBody{fragments: []string{qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`), true)}}
	capture := &qoderEnvelopeTestCapture{}
	err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(body), capture.emit, capture.hooks())
	if err == nil || len(capture.complete) != 0 || !body.closed.Load() {
		t.Fatalf("finish without terminal: err=%v success hooks=%d closed=%v", err, len(capture.complete), body.closed.Load())
	}
}

func TestQoderEnvelopePropagatesCallbackFailure(t *testing.T) {
	for _, failKind := range []provider.ResponseEventKind{provider.EventTextDelta, provider.EventResponseComplete} {
		t.Run(string(failKind), func(t *testing.T) {
			body := &qoderEnvelopeTestBody{fragments: []string{
				qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[{"delta":{"content":"hello"}}]}`), true),
				"data: [DONE]\n\n",
			}}
			capture := &qoderEnvelopeTestCapture{}
			callbackErr := errors.New("client callback failed")
			failures := 0
			err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(body), func(event provider.ResponseEvent) error {
				if failures != 0 {
					t.Error("emitted after callback failure")
				}
				if event.Kind == failKind {
					failures++
					return callbackErr
				}
				return capture.emit(event)
			}, capture.hooks())
			if !errors.Is(err, callbackErr) || failures != 1 || len(capture.complete) != 0 || !body.closed.Load() {
				t.Fatalf("err=%v failures=%d success hooks=%d closed=%v", err, failures, len(capture.complete), body.closed.Load())
			}
		})
	}
}

func TestQoderEnvelopeCancellationStopsFurtherReads(t *testing.T) {
	for _, cancelBefore := range []bool{false, true} {
		name := "during-emit"
		if cancelBefore {
			name = "before-decode"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &qoderEnvelopeTestBody{fragments: []string{
				qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[{"delta":{"content":"hello"}}]}`), true),
			}, tailErr: errors.New("read after cancellation")}
			if cancelBefore {
				cancel()
			}
			capture := &qoderEnvelopeTestCapture{}
			err := qoderEnvelopeTestDecoder(t).Decode(ctx, qoderEnvelopeTestResponse(body), func(event provider.ResponseEvent) error {
				if cancelBefore {
					t.Error("emitted with already canceled context")
				}
				if event.Kind == provider.EventTextDelta {
					cancel()
				}
				return capture.emit(event)
			}, capture.hooks())
			if !errors.Is(err, context.Canceled) || !body.closed.Load() || body.readsAfterEnd != 0 || len(capture.complete) != 0 {
				t.Fatalf("err=%v closed=%v extra reads=%d success hooks=%d", err, body.closed.Load(), body.readsAfterEnd, len(capture.complete))
			}
			if cancelBefore && len(body.fragments) != 1 {
				t.Fatal("already canceled decoder consumed upstream bytes")
			}
		})
	}
}

// Close must release an in-flight upstream Read when the context is canceled.
// The goroutine belongs only to the test harness, not to the framing adapter.
type qoderEnvelopeTestBlockedBody struct {
	reading   chan struct{}
	closed    chan struct{}
	readOnce  sync.Once
	closeOnce sync.Once
}

func (b *qoderEnvelopeTestBlockedBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.reading) })
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *qoderEnvelopeTestBlockedBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestQoderEnvelopeCancellationClosesBlockedUpstreamRead(t *testing.T) {
	decoder := qoderEnvelopeTestDecoder(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &qoderEnvelopeTestBlockedBody{reading: make(chan struct{}), closed: make(chan struct{})}
	defer body.Close()
	capture := &qoderEnvelopeTestCapture{}
	result := make(chan error, 1)
	go func() {
		result <- decoder.Decode(ctx, qoderEnvelopeTestResponse(body), capture.emit, capture.hooks())
	}()
	select {
	case <-body.reading:
	case err := <-result:
		t.Fatalf("decoder returned before attempting upstream read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("decoder did not start upstream read")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked-read cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		body.Close()
		select {
		case <-result:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("context cancellation did not release upstream read")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("cancellation did not close upstream")
	}
	if len(capture.complete) != 0 || len(capture.events) != 0 {
		t.Fatal("canceled blocked read emitted success or events")
	}
}

func TestQoderEnvelopePropagatesUpstreamReadFailure(t *testing.T) {
	readErr := errors.New("upstream transport interrupted")
	body := &qoderEnvelopeTestBody{fragments: []string{
		qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[{"delta":{"content":"partial"}}]}`), true),
	}, tailErr: readErr}
	capture := &qoderEnvelopeTestCapture{}
	err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(body), capture.emit, capture.hooks())
	if !errors.Is(err, readErr) || !body.closed.Load() || len(capture.complete) != 0 {
		t.Fatalf("err=%v closed=%v success hooks=%d", err, body.closed.Load(), len(capture.complete))
	}
}

func TestQoderEnvelopeRejectsNilBody(t *testing.T) {
	capture := &qoderEnvelopeTestCapture{}
	err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(nil), capture.emit, capture.hooks())
	if err == nil || len(capture.events) != 0 || len(capture.complete) != 0 {
		t.Fatal("nil body became success")
	}
}

func TestQoderEnvelopeFramesAcrossByteReadsAndCRLF(t *testing.T) {
	wire := ": keepalive\r\nevent: message\r\n" + strings.ReplaceAll(qoderEnvelopeTestFrame(t, json.RawMessage(`{"choices":[{"delta":{"content":"Huế"}}]}`), true), "\n", "\r\n") + "data: [DONE]\r\n\r\n"
	body := &qoderEnvelopeTestBody{tailErr: errors.New("read after terminal")}
	for i := 0; i < len(wire); i++ {
		body.fragments = append(body.fragments, wire[i:i+1])
	}
	capture := &qoderEnvelopeTestCapture{}
	err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(body), capture.emit, capture.hooks())
	if err != nil {
		t.Fatal(err)
	}
	if !body.closed.Load() || body.readsAfterEnd != 0 || capture.count(provider.EventTextDelta) != 1 || capture.count(provider.EventResponseComplete) != 1 || len(capture.complete) != 1 {
		t.Fatal("fragmented CRLF framing lost text, completion or terminal close")
	}
	for _, event := range capture.events {
		if event.Kind == provider.EventTextDelta && event.Text != "Huế" {
			t.Fatalf("split UTF-8 text = %q", event.Text)
		}
	}
	qoderEnvelopeTestNoPrivateLeak(t, capture)
}

// An endless unterminated line must fail before the finite read budget is
// exhausted. This catches io.ReadAll/unbounded line accumulation without
// coupling the test to a particular scanner buffer implementation or limit.
type qoderEnvelopeTestOversizedBody struct {
	bytes     int
	closed    atomic.Bool
	exhausted error
}

func (b *qoderEnvelopeTestOversizedBody) Read(p []byte) (int, error) {
	const budget = 8 << 20
	if b.bytes >= budget {
		return 0, b.exhausted
	}
	n := len(p)
	if n > budget-b.bytes {
		n = budget - b.bytes
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	prefix := "data: {\"body\":\""
	if b.bytes < len(prefix) {
		copy(p[:n], prefix[b.bytes:])
	}
	b.bytes += n
	return n, nil
}

func (b *qoderEnvelopeTestOversizedBody) Close() error { b.closed.Store(true); return nil }

func TestQoderEnvelopeRejectsOversizedFrameBeforeReadBudget(t *testing.T) {
	body := &qoderEnvelopeTestOversizedBody{exhausted: errors.New("unbounded envelope exceeded read budget")}
	capture := &qoderEnvelopeTestCapture{}
	err := qoderEnvelopeTestDecoder(t).Decode(context.Background(), qoderEnvelopeTestResponse(body), capture.emit, capture.hooks())
	if err == nil || errors.Is(err, body.exhausted) || body.bytes >= 8<<20 {
		t.Fatalf("framing was not bounded: bytes=%d err=%v", body.bytes, err)
	}
	if !body.closed.Load() || len(capture.complete) != 0 || len(capture.events) != 0 {
		t.Fatal("oversized envelope was exposed or completed")
	}
}
