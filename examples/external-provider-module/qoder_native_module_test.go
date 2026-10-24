package externalprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
	"github.com/google/uuid"
)

type qoderNativeOptions struct {
	ModelConfigs map[string]json.RawMessage `json:"modelConfigs"`
}
type qoderNativeConfig struct {
	Key       string `json:"key"`
	Reasoning bool   `json:"is_reasoning"`
	MaxOutput int    `json:"max_output_tokens"`
}
type qoderNativeCodec struct{ options qoderNativeOptions }
type qoderNativeModule struct{}

func (qoderNativeModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.qoder-native", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}
func (qoderNativeModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterEndpoint(qoderNativeEndpoint{}); err != nil {
		return err
	}
	if err := runtime.RegisterAuthFactory("example.qoder-auth", func(provider.AuthOptions) (provider.AuthFlow, error) { return qoderFixtureAuth{}, nil }); err != nil {
		return err
	}
	schema := json.RawMessage(`{"type":"object","properties":{"modelConfigs":{"type":"object","minProperties":1,"maxProperties":128,"additionalProperties":{"type":"object","properties":{"key":{"type":"string","minLength":1,"maxLength":128},"is_reasoning":{"type":"boolean"},"max_output_tokens":{"type":"integer","minimum":1,"maximum":1048576}},"required":["key","max_output_tokens"],"additionalProperties":true}}},"required":["modelConfigs"],"additionalProperties":false}`)
	if err := runtime.RegisterConfiguredRequestCodec(qoderNativeCodecID, 1, provider.Ref{Kind: provider.SchemaKind, ID: "example.qoder-native.options", ContractVersion: 1}, schema, func(raw json.RawMessage) (any, error) {
		var options qoderNativeOptions
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, err
		}
		for key, value := range options.ModelConfigs {
			var config qoderNativeConfig
			if err := json.Unmarshal(value, &config); err != nil || config.Key != key {
				return nil, errors.New("native model config key mismatch")
			}
		}
		return qoderNativeCodec{options: options}, nil
	}); err != nil {
		return err
	}
	builtins, err := provider.NewRuntimeRegistry()
	if err != nil {
		return err
	}
	catalog, err := builtins.FreezeCatalog()
	if err != nil {
		return err
	}
	_, value, err := catalog.Bind(provider.Ref{Kind: string(provider.PrimitiveResponseDecoder), ID: "openai-sse", ContractVersion: 1}, nil)
	if err != nil {
		return err
	}
	decoder, ok := value.(provider.ResponseDecoder)
	if !ok {
		return errors.New("registered OpenAI decoder has invalid type")
	}
	return runtime.RegisterResponseDecoder(qoderEnvelopeDecoder{delegate: decoder})
}

func (qoderNativeCodec) ID() string { return qoderNativeCodecID }
func (c qoderNativeCodec) model(route provider.Route) (qoderNativeConfig, json.RawMessage, error) {
	raw, ok := c.options.ModelConfigs[route.ExternalModel]
	if !ok {
		return qoderNativeConfig{}, nil, errors.New("native model configuration unavailable")
	}
	var config qoderNativeConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, nil, err
	}
	return config, raw, nil
}
func (c qoderNativeCodec) DescribeCompatibility(input provider.CompatibilityContext) []provider.FacetMapping {
	config, _, modelErr := c.model(input.Route)
	if modelErr == nil {
		modelErr = c.check(input.Request, input.Route)
	}
	result := make([]provider.FacetMapping, 0, len(input.Policy.RequiredFacets))
	for _, facet := range input.Policy.RequiredFacets {
		if provider.IsResponseCompatibilityFacet(facet) {
			continue
		}
		mapping := provider.FacetMapping{Facet: facet, Disposition: provider.FacetUnsupported, Paths: []string{"request"}, Reason: "native text fixture has no declared mapping for this facet"}
		if modelErr == nil && input.Request.SourceFormat == provider.FormatOpenAIChat {
			switch facet {
			case provider.FacetWireRequest, provider.FacetPromptLayers:
				mapping.Disposition = provider.FacetTranslated
				mapping.Reason = "typed text payload is mapped without dropping source fields"
			case provider.FacetToolDefinitions:
				valid := true
				for _, tool := range input.Request.Tools {
					if tool.Type != "function" && tool.Type != "" {
						valid = false
					}
				}
				if valid {
					mapping.Disposition = provider.FacetTranslated
				}
			case provider.FacetGenerationOptions:
				g := input.Request.Generation
				if len(g.Unsupported) == 0 && g.Temperature == nil && g.TopP == nil && len(g.StopSequences) == 0 && (g.MaxOutputTokens == nil || *g.MaxOutputTokens > 0 && *g.MaxOutputTokens <= config.MaxOutput) {
					mapping.Disposition = provider.FacetTranslated
				}
			}
		}
		result = append(result, mapping)
	}
	return result
}

type qoderInputFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}
type qoderInputTool struct {
	Type     string             `json:"type"`
	Function qoderInputFunction `json:"function"`
}
type qoderTextInput struct {
	Model         string               `json:"model"`
	Messages      []qoderNativeMessage `json:"messages"`
	Tools         []qoderInputTool     `json:"tools,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	MaxTokens     json.RawMessage      `json:"max_tokens,omitempty"`
	MaxCompletion json.RawMessage      `json:"max_completion_tokens,omitempty"`
	MaxOutput     json.RawMessage      `json:"max_output_tokens,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

func qoderStrictSubset(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("invalid trailing input")
	}
	return nil
}
func (c qoderNativeCodec) check(request provider.NormalizedRequest, route provider.Route) error {
	if _, err := qoderOrigin(route.BaseURL); err != nil {
		return err
	}
	if request.SourceFormat != provider.FormatOpenAIChat || len(request.Extensions) > 0 || len(request.UnsupportedFacets) > 0 || len(request.Artifacts) > 0 || len(request.OperationPayload) > 0 {
		return errors.New("native text contract does not preserve this source envelope")
	}
	if len(request.Raw) > 0 {
		raw, err := json.Marshal(request.Raw)
		if err != nil {
			return err
		}
		var input qoderTextInput
		if err := qoderStrictSubset(raw, &input); err != nil {
			return errors.New("native text contract does not preserve these options/messages")
		}
	}
	for _, layer := range request.Prompt.Layers {
		if layer.Role != "system" || len(layer.Parts) > 0 {
			return errors.New("native text contract requires plain system prompt")
		}
	}
	for _, message := range request.Messages {
		if _, ok := message.Content.(string); !ok || message.Role != "user" && message.Role != "assistant" || message.Name != "" || message.ToolCallID != "" || len(message.ToolCalls) > 0 {
			return errors.New("native text contract cannot preserve this message")
		}
	}
	for _, tool := range request.Tools {
		if tool.Type != "" && tool.Type != "function" {
			return errors.New("unsupported tool type")
		}
		raw, err := json.Marshal(tool.Function)
		if err != nil {
			return err
		}
		var function qoderInputFunction
		if err := qoderStrictSubset(raw, &function); err != nil || function.Name == "" || function.Strict != nil && *function.Strict {
			return errors.New("unsupported function semantics")
		}
	}
	return nil
}

func qoderOrigin(base string) (*url.URL, error) {
	origin, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if origin.Scheme != "http" && origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, errors.New("native signing requires an origin-only base URL")
	}
	return origin, nil
}

type qoderNativeEndpoint struct{}

func (qoderNativeEndpoint) ID() string { return "example.qoder-native-endpoint" }
func (qoderNativeEndpoint) Resolve(base, path string, options provider.EndpointOptions) (string, error) {
	origin, err := qoderOrigin(base)
	if err != nil {
		return "", err
	}
	if path != qoderFixturePath || options.Path != "" || len(options.Query) != 0 {
		return "", errors.New("native signing forbids endpoint path/query overrides")
	}
	relative, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	return origin.ResolveReference(relative).String(), nil
}

type qoderNativeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type qoderNativeTool struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}
type qoderNativePayload struct {
	RequestID      string               `json:"request_id"`
	RequestSetID   string               `json:"request_set_id"`
	ChatRecordID   string               `json:"chat_record_id"`
	SessionID      string               `json:"session_id"`
	Stream         bool                 `json:"stream"`
	ChatTask       string               `json:"chat_task"`
	IsReply        bool                 `json:"is_reply"`
	IsRetry        bool                 `json:"is_retry"`
	Source         int                  `json:"source"`
	Version        string               `json:"version"`
	SessionType    string               `json:"session_type"`
	AgentID        string               `json:"agent_id"`
	TaskID         string               `json:"task_id"`
	CodeLanguage   string               `json:"code_language"`
	ChatPrompt     string               `json:"chat_prompt"`
	ImageURLs      any                  `json:"image_urls"`
	AliyunUserType string               `json:"aliyun_user_type"`
	System         string               `json:"system"`
	Messages       []qoderNativeMessage `json:"messages"`
	Tools          []qoderNativeTool    `json:"tools"`
	Parameters     struct {
		MaxTokens int `json:"max_tokens"`
	} `json:"parameters"`
	ModelConfig json.RawMessage `json:"model_config"`
	ChatContext struct {
		ChatPrompt string `json:"chatPrompt"`
		ImageURLs  any    `json:"imageUrls"`
		Extra      struct {
			Context     []string `json:"context"`
			ModelConfig struct {
				Key       string `json:"key"`
				Reasoning bool   `json:"is_reasoning"`
			} `json:"modelConfig"`
			OriginalContent string `json:"originalContent"`
		} `json:"extra"`
		Features []string `json:"features"`
		Text     string   `json:"text"`
	} `json:"chat_context"`
	Business struct {
		Product string `json:"product"`
		Version string `json:"version"`
		Type    string `json:"type"`
		Stage   string `json:"stage"`
		ID      string `json:"id"`
		Name    string `json:"name"`
		BeginAt int64  `json:"begin_at"`
	} `json:"business"`
}

func qoderOpaqueID(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

func (c qoderNativeCodec) Prepare(ctx context.Context, request provider.NormalizedRequest, route provider.Route, credential provider.Credential) (provider.UpstreamRequest, error) {
	if err := c.check(request, route); err != nil {
		return provider.UpstreamRequest{}, err
	}
	config, rawConfig, err := c.model(route)
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	payload := qoderNativePayload{RequestID: uuid.NewString(), Stream: true, ChatTask: "FREE_INPUT", IsReply: true, Source: 1, Version: "3", SessionType: "qodercli", AgentID: "agent_common", TaskID: "common", Messages: []qoderNativeMessage{}, Tools: []qoderNativeTool{}, ModelConfig: append(json.RawMessage(nil), rawConfig...)}
	var prompts []string
	for _, layer := range request.Prompt.Layers {
		if len(layer.Parts) != 0 {
			return provider.UpstreamRequest{}, errors.New("native text fixture requires plain prompt text")
		}
		prompts = append(prompts, layer.Text)
	}
	payload.System = strings.Join(prompts, "\n\n")
	lastUser := ""
	for _, message := range request.Messages {
		content, ok := message.Content.(string)
		if !ok || message.Role != "user" && message.Role != "assistant" || len(message.ToolCalls) != 0 {
			return provider.UpstreamRequest{}, errors.New("native text fixture cannot encode this message")
		}
		payload.Messages = append(payload.Messages, qoderNativeMessage{Role: message.Role, Content: content})
		if message.Role == "user" {
			lastUser = content
		}
	}
	for _, tool := range request.Tools {
		payload.Tools = append(payload.Tools, qoderNativeTool{Type: "function", Function: tool.Function})
	}
	maxTokens := config.MaxOutput
	if maxTokens <= 0 {
		maxTokens = 32768
	}
	if request.Generation.MaxOutputTokens != nil {
		maxTokens = *request.Generation.MaxOutputTokens
		if maxTokens <= 0 || maxTokens > config.MaxOutput {
			return provider.UpstreamRequest{}, errors.New("requested output exceeds selected native configuration")
		}
	}
	payload.Parameters.MaxTokens = maxTokens
	payload.SessionID, err = qoderOpaqueID(struct{ Connection, Endpoint, User, Model, ClientSession string }{credential.ConnectionID, route.BaseURL, credential.AuthContext["userId"], route.ExternalModel, request.Session.ID})
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	payload.ChatRecordID, err = qoderOpaqueID(struct {
		Session, System string
		Messages        []qoderNativeMessage
		Tools           []qoderNativeTool
		MaxTokens       int
	}{payload.SessionID, payload.System, payload.Messages, payload.Tools, maxTokens})
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	payload.RequestSetID = payload.ChatRecordID
	payload.ChatContext.Extra.Context = []string{}
	payload.ChatContext.Extra.ModelConfig.Key = config.Key
	payload.ChatContext.Extra.ModelConfig.Reasoning = config.Reasoning
	payload.ChatContext.Extra.OriginalContent = lastUser
	payload.ChatContext.Features = []string{}
	payload.ChatContext.Text = lastUser
	payload.Business.Product = "cli"
	payload.Business.Version = "1.0.0"
	payload.Business.Type = "agent"
	payload.Business.Stage = "start"
	payload.Business.ID = uuid.NewString()
	payload.Business.Name = "GoBroom request"
	payload.Business.BeginAt = time.Now().UnixMilli()
	plain, err := json.Marshal(payload)
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	return qoderPreparePayload(ctx, request, route, credential, plain)
}

// Shared byte signer for the signing-mechanics and source-shaped payload tests.
func qoderPreparePayload(ctx context.Context, request provider.NormalizedRequest, route provider.Route, credential provider.Credential, plain []byte) (provider.UpstreamRequest, error) {
	if err := credential.ValidateRuntimeContext(); err != nil {
		return provider.UpstreamRequest{}, err
	}
	if credential.AuthContext["userId"] == "" || credential.Secret == "" {
		return provider.UpstreamRequest{}, errors.New("native signing requires user identity and token")
	}
	body := qoderFixtureEncodeBody(plain)
	headers := http.Header{"Content-Type": {"application/json"}, "Accept": {"text/event-stream"}, "Accept-Encoding": {"identity"}, "Cache-Control": {"no-cache"}, "X-Model-Key": {route.ExternalModel}, "X-Model-Source": {"system"}}
	origin, err := qoderOrigin(route.BaseURL)
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	relative, err := url.Parse(qoderFixturePath)
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	absolute := origin.ResolveReference(relative).String()
	authorize := func(_ context.Context, headers http.Header, reader io.Reader, selected provider.Credential) error {
		encoded, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		signed, err := qoderFixtureCOSYHeaders(encoded, absolute, selected)
		if err != nil {
			return fmt.Errorf("sign encoded native request: %w", err)
		}
		maps.Copy(headers, signed)
		return nil
	}
	return provider.PrepareBytesRequest(ctx, http.MethodPost, qoderFixturePath, headers, body, credential, authorize, request.Transport.IdempotencyKey != "")
}
