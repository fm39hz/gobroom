package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestChatAdapterForwardsAndPassthroughsSSE(t *testing.T) {
	adapter := Chat{}
	request := normalize.Request{Model: "public", Stream: true, Raw: map[string]any{"model": "public", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}}
	route := kernel.Route{ID: "route:test", BaseURL: "https://provider.example/v1", ExternalModel: "upstream-model", Enabled: true}
	prepared, err := adapter.Prepare(context.Background(), request, route, kernel.Credential{Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != "https://provider.example/v1/chat/completions" || prepared.Headers.Get("Authorization") != "Bearer secret" {
		t.Fatalf("prepared=%#v", prepared)
	}
	response := kernel.UpstreamResponse{Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: ok\n\n"))}
	recorder := httptest.NewRecorder()
	if err := adapter.TranslateStream(context.Background(), response, recorder, normalize.FormatOpenAIChat, kernel.StreamHooks{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), "data:") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}
