package normalize

import (
	"net/http"
	"testing"
)

func TestChatStreamOptionsBecomeImmutableClientTransportHints(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, include := range []bool{false, true} {
			result, err := Map("/v1/chat/completions", http.Header{}, map[string]any{
				"model": "role", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
				"stream": stream, "stream_options": map[string]any{"include_usage": include},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Request.Transport.IncludeUsage != (stream && include) {
				t.Fatalf("wrong usage hint: %#v", result.Request.Transport)
			}
			if _, exists := result.Request.Extensions["stream_options"]; exists {
				t.Fatal("client rendering preference leaked into provider extensions")
			}
		}
	}
}

func TestChatStreamOptionsRejectInvalidOrUnsupportedBindings(t *testing.T) {
	for _, options := range []any{true, "true", []any{}, map[string]any{"include_usage": "true"}, map[string]any{"include_usage": true, "unknown": true}} {
		_, err := Map("/v1/chat/completions", http.Header{}, map[string]any{"model": "role", "messages": []any{}, "stream": true, "stream_options": options})
		if err == nil {
			t.Fatalf("invalid stream options accepted: %#v", options)
		}
	}
}
