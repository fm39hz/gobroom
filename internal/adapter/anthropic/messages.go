package anthropic

import (
	"bufio"
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

type Messages struct{ Client *http.Client }

const defaultUpstreamTimeout = 2 * time.Minute

func (Messages) ID() string                { return "anthropic-messages" }
func (Messages) Protocol() kernel.Protocol { return kernel.ProtocolAnthropic }

type messagesRequestCodec struct{ adapter Messages }

func (c messagesRequestCodec) ID() string { return "anthropic-messages-json" }
func (c messagesRequestCodec) Prepare(ctx context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	return c.adapter.Prepare(ctx, request, route, credential)
}

type messagesResponseCodec struct{ adapter Messages }

func (c messagesResponseCodec) ID() string { return "anthropic-sse" }
func (c messagesResponseCodec) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.adapter.ClassifyError(status, body)
}
func (c messagesResponseCodec) TranslateStream(ctx context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks kernel.StreamHooks) error {
	return c.adapter.TranslateStream(ctx, response, writer, source, hooks)
}

func NewAdapter() kernel.ProviderAdapter {
	adapter := Messages{}
	return kernel.ComposedAdapter{AdapterID: "anthropic-messages", AdapterProtocol: kernel.ProtocolAnthropic, Request: messagesRequestCodec{adapter}, Transport: kernel.HTTPTransport{}, Response: messagesResponseCodec{adapter}}
}

func (a Messages) Prepare(_ context.Context, request kernel.NormalizedRequest, route kernel.Route, credential kernel.Credential) (kernel.UpstreamRequest, error) {
	if route.BaseURL == "" {
		return kernel.UpstreamRequest{}, fmt.Errorf("route %s has no base URL", route.ID)
	}
	url := strings.TrimRight(route.BaseURL, "/")
	if !strings.HasSuffix(url, "/messages") {
		url += "/messages"
	}
	body := map[string]any{}
	if request.SourceFormat == normalize.FormatAnthropic {
		for key, value := range request.Raw {
			body[key] = value
		}
	} else {
		body["messages"] = make([]map[string]any, 0, len(request.Messages))
		for _, message := range request.Messages {
			body["messages"] = append(body["messages"].([]map[string]any), map[string]any{"role": message.Role, "content": message.Content})
		}
		if len(request.Tools) > 0 {
			body["tools"] = anthropicTools(request.Tools)
		}
	}
	body["model"] = route.ExternalModel
	body["stream"] = request.Stream
	if _, ok := body["max_tokens"]; !ok {
		body["max_tokens"] = 4096
	}
	if request.SourceFormat != normalize.FormatAnthropic {
		applyAnthropicThinking(body, request.Thinking)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return kernel.UpstreamRequest{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("anthropic-version", "2023-06-01")
	secret := credential.Secret
	if secret != "" {
		headers.Set("x-api-key", secret)
	}
	return kernel.UpstreamRequest{Method: http.MethodPost, URL: url, Headers: headers, Body: bytes.NewReader(data)}, nil
}

func applyAnthropicThinking(body map[string]any, intent normalize.ThinkingIntent) {
	translated := normalize.AnthropicReasoning(intent)
	for key, value := range translated {
		body[key] = value
	}
	if intent.Mode == "budget" && intent.BudgetTokens > 0 {
		if max, ok := body["max_tokens"].(float64); !ok || int(max) <= intent.BudgetTokens {
			body["max_tokens"] = intent.BudgetTokens + 1024
		}
	}
}

func (a Messages) Execute(ctx context.Context, request kernel.UpstreamRequest) (kernel.UpstreamResponse, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: defaultUpstreamTimeout}
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

func (Messages) ClassifyError(status int, body []byte) kernel.ErrorClass {
	lower := strings.ToLower(string(body))
	if status == 401 || status == 403 || strings.Contains(lower, "authentication") || strings.Contains(lower, "api key") {
		return kernel.ErrorAuth
	}
	if status == 408 || status == 409 || status == 429 || status >= 500 || strings.Contains(lower, "rate limit") || strings.Contains(lower, "quota") {
		return kernel.ErrorCooldown
	}
	if status >= 400 {
		return kernel.ErrorTerminal
	}
	return ""
}
func (a Messages) TranslateStream(_ context.Context, response kernel.UpstreamResponse, writer http.ResponseWriter, source normalize.Format, hooks kernel.StreamHooks) error {
	if source == normalize.FormatAnthropic {
		copyHeaders(writer, response.Headers)
		writer.WriteHeader(response.Status)
		if hooks.OnFirstByte != nil {
			hooks.OnFirstByte(time.Now())
		}
		_, err := io.Copy(writer, response.Body)
		if err == nil && hooks.OnComplete != nil {
			hooks.OnComplete(kernel.UsageEvent{Status: "ok"})
		}
		if err != nil && hooks.OnError != nil {
			hooks.OnError(err)
		}
		return err
	}
	contentType := strings.ToLower(response.Headers.Get("content-type"))
	if strings.Contains(contentType, "text/event-stream") || response.Body != nil {
		if strings.Contains(contentType, "text/event-stream") {
			return translateAnthropicSSE(response.Body, writer, hooks)
		}
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	err = writeOpenAIJSON(writer, response.Status, payload)
	if err == nil && hooks.OnComplete != nil {
		hooks.OnComplete(kernel.UsageEvent{Status: "ok"})
	}
	if err != nil && hooks.OnError != nil {
		hooks.OnError(err)
	}
	return err
}

func copyHeaders(writer http.ResponseWriter, headers http.Header) {
	for key, values := range headers {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
}

func translateAnthropicSSE(body io.Reader, writer http.ResponseWriter, hooks kernel.StreamHooks) error {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	started := false
	toolIndex := 0
	var inputTokens, outputTokens int64
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			_, _ = io.WriteString(writer, "data: [DONE]\n\n")
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			if hooks.OnEvent != nil {
				hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventResponseError, Error: "malformed SSE JSON: " + err.Error()})
			}
			if hooks.OnError != nil {
				hooks.OnError(err)
			}
			return err
		}
		eventType, _ := event["type"].(string)
		if eventType == "message_start" {
			message, _ := event["message"].(map[string]any)
			usage, _ := message["usage"].(map[string]any)
			inputTokens = int64(numberValue(usage["input_tokens"]))
		}
		if eventType == "content_block_start" {
			block, _ := event["content_block"].(map[string]any)
			if block["type"] == "tool_use" {
				if hooks.OnEvent != nil {
					hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventToolCallDelta, Index: toolIndex, ToolCallID: stringValue(block["id"]), ToolName: stringValue(block["name"])})
				}
				call := map[string]any{"index": toolIndex, "id": block["id"], "type": "function", "function": map[string]any{"name": block["name"]}}
				chunk := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}}}}
				writeSSEChunk(writer, chunk)
				toolIndex++
			}
		}
		if eventType == "content_block_delta" {
			delta, _ := event["delta"].(map[string]any)
			text, _ := delta["text"].(string)
			if text != "" {
				if hooks.OnEvent != nil {
					hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventTextDelta, Text: text})
				}
				chunk := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}
				encoded, _ := json.Marshal(chunk)
				_, _ = io.WriteString(writer, "data: "+string(encoded)+"\n\n")
				if !started && hooks.OnFirstByte != nil {
					hooks.OnFirstByte(time.Now())
					started = true
				}
			}
			if partial, ok := delta["partial_json"].(string); ok {
				if hooks.OnEvent != nil {
					hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventToolCallDelta, Index: toolIndex - 1, ToolArguments: partial})
				}
				call := map[string]any{"index": toolIndex - 1, "function": map[string]any{"arguments": partial}}
				chunk := map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}}}}
				writeSSEChunk(writer, chunk)
			}
		}
		if eventType == "message_delta" {
			delta, _ := event["delta"].(map[string]any)
			usage, _ := event["usage"].(map[string]any)
			outputTokens = int64(numberValue(usage["output_tokens"]))
			if hooks.OnEvent != nil {
				hooks.OnEvent(kernel.ResponseEvent{At: time.Now(), Kind: kernel.EventUsage, Usage: &kernel.UsageEvent{InputTokens: inputTokens, OutputTokens: outputTokens}})
			}
			finish := "stop"
			if delta["stop_reason"] == "tool_use" {
				finish = "tool_calls"
			}
			writeSSEChunk(writer, map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
		}
		if eventType == "message_stop" {
			_, _ = io.WriteString(writer, "data: [DONE]\n\n")
			if hooks.OnComplete != nil {
				hooks.OnComplete(kernel.UsageEvent{Status: "ok", InputTokens: inputTokens, OutputTokens: outputTokens})
			}
		}
	}
	if err := scanner.Err(); err != nil && hooks.OnError != nil {
		hooks.OnError(err)
	}
	return scanner.Err()
}

func writeSSEChunk(writer http.ResponseWriter, chunk map[string]any) {
	encoded, _ := json.Marshal(chunk)
	_, _ = io.WriteString(writer, "data: "+string(encoded)+"\n\n")
}

func anthropicTools(tools []normalize.Tool) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		fn := tool.Function
		if fn == nil {
			fn = map[string]any{}
		}
		name, _ := fn["name"].(string)
		if name == "" {
			name = tool.Name
		}
		item := map[string]any{"name": name}
		if description, ok := fn["description"].(string); ok {
			item["description"] = description
		}
		if schema, ok := fn["parameters"]; ok {
			item["input_schema"] = schema
		} else {
			item["input_schema"] = map[string]any{"type": "object"}
		}
		result = append(result, item)
	}
	return result
}

func numberValue(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func stringValue(value any) string { result, _ := value.(string); return result }

func writeOpenAIJSON(writer http.ResponseWriter, status int, payload map[string]any) error {
	text := ""
	if blocks, ok := payload["content"].([]any); ok {
		for _, block := range blocks {
			item, _ := block.(map[string]any)
			if value, ok := item["text"].(string); ok {
				text += value
			}
		}
	}
	result := map[string]any{"id": "chatcmpl-gobroom", "object": "chat.completion", "created": time.Now().Unix(), "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": payload["stop_reason"]}}}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, err = writer.Write(encoded)
	return err
}
