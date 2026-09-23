package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/normalize"
)

type Gemini struct{ Client *http.Client }

func (Gemini) ID() string                { return "gemini" }
func (Gemini) Protocol() kernel.Protocol { return kernel.ProtocolGemini }
func NewAdapter() kernel.ProviderAdapter { return Gemini{} }

func (Gemini) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	if route.BaseURL == "" {
		return kernel.UpstreamRequest{}, fmt.Errorf("route %s has no base URL", route.ID)
	}
	url := strings.TrimRight(route.BaseURL, "/")
	if !strings.Contains(url, ":generateContent") {
		url += "/models/" + route.ExternalModel + ":generateContent"
	}
	body := map[string]any{}
	for key, value := range request.Raw {
		body[key] = value
	}
	if _, ok := body["contents"]; !ok {
		contents := make([]map[string]any, 0, len(request.Messages))
		for _, message := range request.Messages {
			contents = append(contents, map[string]any{"role": geminiRole(message.Role), "parts": []map[string]any{{"text": fmt.Sprint(message.Content)}}})
		}
		body["contents"] = contents
	}
	for key, value := range normalize.GeminiReasoning(request.Thinking) {
		body[key] = value
	}
	data, err := json.Marshal(body)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if credential.Secret != "" {
		headers.Set("x-goog-api-key", credential.Secret)
	}
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: url, Headers: headers, Body: bytes.NewReader(data)}, nil
}

func geminiRole(role string) string {
	if role == "assistant" {
		return "model"
	}
	return "user"
}

func (a Gemini) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, request.Body)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	req.Header = request.Headers
	response, err := client.Do(req)
	if err != nil {
		return kernel.UpstreamResponse{}, err
	}
	return kernel.UpstreamResponse{Status: response.StatusCode, Headers: response.Header, Body: response.Body}, nil
}

func (Gemini) ClassifyError(status int, _ []byte) kernel.ErrorClass {
	if status == 401 || status == 403 {
		return kernel.ErrorAuth
	}
	if status == 429 || status >= 500 {
		return kernel.ErrorCooldown
	}
	if status >= 400 {
		return kernel.ErrorTerminal
	}
	return kernel.ErrorRetryable
}

func (Gemini) TranslateStream(_ context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, _ normalize.Format, hooks kernel.StreamHooks) error {
	data, err := io.ReadAll(response.Body)
	if err != nil {
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	}
	text := ""
	if candidates, ok := payload["candidates"].([]any); ok && len(candidates) > 0 {
		candidate, _ := candidates[0].(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		if parts, ok := content["parts"].([]any); ok {
			for _, raw := range parts {
				part, _ := raw.(map[string]any)
				text += stringValue(part["text"])
			}
		}
	}
	result := map[string]any{"id": "chatcmpl-gemini", "object": "chat.completion", "created": time.Now().Unix(), "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(response.Status)
	if hooks.OnFirstByte != nil {
		hooks.OnFirstByte(time.Now())
	}
	if err := json.NewEncoder(writer).Encode(result); err != nil {
		if hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	}
	if hooks.OnEvent != nil {
		hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventTextDelta, Text: text})
	}
	if hooks.OnComplete != nil {
		hooks.OnComplete(kernel.UsageEvent{Status: "ok"})
	}
	return nil
}

func stringValue(value any) string { result, _ := value.(string); return result }
