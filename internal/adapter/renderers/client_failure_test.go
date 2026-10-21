package renderers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func testUpstreamFailure(message string) kernel.ClientFailure {
	return kernel.ClientFailure{Code: kernel.ClientFailureUpstream, Type: "upstream_error", Message: message}
}

func TestContinuityFailureProjectsCanonicalCode(t *testing.T) {
	failure := kernel.ClientFailureForError(kernel.ErrContinuityRestartRequired)
	if !failure.Valid() {
		t.Fatal("continuity failure is invalid")
	}
	if failure.Code != kernel.ClientFailureContinuityRestart {
		t.Fatalf("continuity code = %q, want descriptor %q", failure.Code, kernel.ClientFailureContinuityRestart)
	}
	anthropicType := "api_error"
	if failure.Type == "invalid_request_error" {
		anthropicType = failure.Type
	}

	for _, tt := range []struct {
		format normalize.Format
		want   map[string]any
	}{
		{normalize.FormatOpenAIChat, map[string]any{"code": failure.Code, "type": failure.Type, "message": failure.Message}},
		{normalize.FormatOpenAIResponses, map[string]any{"code": failure.Code, "type": failure.Type, "message": failure.Message}},
		{normalize.FormatAnthropic, map[string]any{"type": anthropicType, "message": failure.Message, "details": map[string]any{"error_code": failure.Code}}},
	} {
		t.Run(string(tt.format), func(t *testing.T) {
			writer := httptest.NewRecorder()
			if err := writeProtocolError(writer, tt.format, failure); err != nil {
				t.Fatal(err)
			}
			body := writer.Body.String()
			if strings.Contains(body, "provider_code") || strings.Contains(body, "raw_provider") {
				t.Fatalf("provider fields leaked: %s", body)
			}
			line := strings.TrimSpace(body)
			if strings.HasPrefix(line, "event: ") {
				line = strings.TrimSpace(strings.SplitN(line, "\n", 2)[1])
				line = strings.TrimPrefix(line, "data: ")
			} else {
				line = strings.TrimPrefix(line, "data: ")
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(line), &envelope); err != nil {
				t.Fatalf("invalid error JSON %q: %v", body, err)
			}
			var got any
			switch tt.format {
			case normalize.FormatOpenAIChat:
				got = envelope["error"]
			case normalize.FormatOpenAIResponses:
				got = envelope["response"].(map[string]any)["error"]
			case normalize.FormatAnthropic:
				got = envelope["error"]
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			var gotValue, wantValue any
			_ = json.Unmarshal(gotJSON, &gotValue)
			_ = json.Unmarshal(wantJSON, &wantValue)
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("error projection = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestClientFailureValueIsCopiedIntoRendererState(t *testing.T) {
	options := kernel.ResponseRenderContext{Status: 200, ClientFormat: normalize.FormatOpenAIChat, ProviderFormat: normalize.FormatOpenAIChat, Streaming: true, ProviderStreaming: true}
	writer := httptest.NewRecorder()
	session, err := (OpenAIChat{}).Begin(context.Background(), options, writer)
	if err != nil {
		t.Fatal(err)
	}
	failure := testUpstreamFailure("original")
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventResponseError, Error: failure}); err != nil {
		t.Fatal(err)
	}
	failure.Message = "mutated"
	if err := session.Emit(context.Background(), kernel.ResponseEvent{Kind: kernel.EventTextDelta, Text: "ignored"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "original") || strings.Contains(err.Error(), "mutated") {
		t.Fatalf("renderer failure did not retain value copy: %v", err)
	}
}
