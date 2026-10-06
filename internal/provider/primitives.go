package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fm39hz/gobroom/internal/adapter/anthropic"
	"github.com/fm39hz/gobroom/internal/adapter/gemini"
	openai "github.com/fm39hz/gobroom/internal/adapter/openai"
	"github.com/fm39hz/gobroom/internal/kernel"
)

type Operation string

const (
	OperationChat       Operation = "chat"
	OperationResponses  Operation = "responses"
	OperationMessages   Operation = "messages"
	OperationModels     Operation = "models"
	OperationEmbeddings Operation = "embeddings"
	OperationImage      Operation = "image"
	OperationAudio      Operation = "audio"
	OperationVideo      Operation = "video"
	OperationUsage      Operation = "usage"
	OperationQuota      Operation = "quota"
)

type PrimitiveKind string

const (
	PrimitiveEndpoint        PrimitiveKind = "endpoint"
	PrimitiveTransport       PrimitiveKind = "transport"
	PrimitiveAuth            PrimitiveKind = "auth"
	PrimitiveRequestCodec    PrimitiveKind = "request_codec"
	PrimitiveResponseCodec   PrimitiveKind = "response_codec"
	PrimitiveModelSource     PrimitiveKind = "model_source"
	PrimitiveUsageSource     PrimitiveKind = "usage_source"
	PrimitiveQuotaSource     PrimitiveKind = "quota_source"
	PrimitiveSessionStore    PrimitiveKind = "session_store"
	PrimitiveErrorClassifier PrimitiveKind = "error_classifier"
	PrimitiveExtension       PrimitiveKind = "extension"
)

type PrimitiveRef struct {
	Kind PrimitiveKind `json:"kind"`
	ID   string        `json:"id"`
}

// ErrorClassifierOptions configure a shared classifier for a provider's
// upstream wire shape. Options remain declarative data; the kernel only sees
// ClassifiedOutcome values.
type ErrorClassifierOptions struct {
	HTTPJSON *HTTPJSONErrorClassifierOptions `json:"httpJson,omitempty"`
}

type HTTPJSONErrorClassifierOptions struct {
	DefaultScope        kernel.OutcomeScope `json:"defaultScope,omitempty"`
	AuthScope           kernel.OutcomeScope `json:"authScope,omitempty"`
	QuotaScope          kernel.OutcomeScope `json:"quotaScope,omitempty"`
	RateLimitScope      kernel.OutcomeScope `json:"rateLimitScope,omitempty"`
	CapacityScope       kernel.OutcomeScope `json:"capacityScope,omitempty"`
	CodePath            string              `json:"codePath,omitempty"`
	TypePath            string              `json:"typePath,omitempty"`
	StatusPath          string              `json:"statusPath,omitempty"`
	MessagePath         string              `json:"messagePath,omitempty"`
	ResetAtPath         string              `json:"resetAtPath,omitempty"`
	RetryAfterPath      string              `json:"retryAfterPath,omitempty"`
	RetryDelayPath      string              `json:"retryDelayPath,omitempty"`
	WindowNamePath      string              `json:"windowNamePath,omitempty"`
	WindowLimitPath     string              `json:"windowLimitPath,omitempty"`
	WindowUsedPath      string              `json:"windowUsedPath,omitempty"`
	WindowRemainingPath string              `json:"windowRemainingPath,omitempty"`
	QuotaWindowName     string              `json:"quotaWindowName,omitempty"`
	QuotaWindowKind     string              `json:"quotaWindowKind,omitempty"`
	QuotaCodes          []string            `json:"quotaCodes,omitempty"`
	QuotaTypes          []string            `json:"quotaTypes,omitempty"`
	QuotaStatuses       []string            `json:"quotaStatuses,omitempty"`
	QuotaMessageTokens  []string            `json:"quotaMessageTokens,omitempty"`
}

func (o ErrorClassifierOptions) Empty() bool {
	return o.HTTPJSON == nil || o.HTTPJSON.Empty()
}

func (o HTTPJSONErrorClassifierOptions) Empty() bool {
	return o.DefaultScope == "" && o.AuthScope == "" && o.QuotaScope == "" && o.RateLimitScope == "" && o.CapacityScope == "" && o.CodePath == "" && o.TypePath == "" && o.StatusPath == "" && o.MessagePath == "" && o.ResetAtPath == "" && o.RetryAfterPath == "" && o.RetryDelayPath == "" && o.WindowNamePath == "" && o.WindowLimitPath == "" && o.WindowUsedPath == "" && o.WindowRemainingPath == "" && o.QuotaWindowName == "" && o.QuotaWindowKind == "" && len(o.QuotaCodes) == 0 && len(o.QuotaTypes) == 0 && len(o.QuotaStatuses) == 0 && len(o.QuotaMessageTokens) == 0
}

func (o HTTPJSONErrorClassifierOptions) Validate() error {
	for name, scope := range map[string]kernel.OutcomeScope{
		"defaultScope": o.DefaultScope, "authScope": o.AuthScope, "quotaScope": o.QuotaScope,
		"rateLimitScope": o.RateLimitScope, "capacityScope": o.CapacityScope,
	} {
		if scope != "" && !validOutcomeScope(scope) {
			return fmt.Errorf("error classifier %s has unsupported scope %q", name, scope)
		}
	}
	for _, item := range []struct{ name, path string }{
		{"codePath", o.CodePath}, {"typePath", o.TypePath}, {"statusPath", o.StatusPath},
		{"messagePath", o.MessagePath}, {"resetAtPath", o.ResetAtPath},
		{"retryAfterPath", o.RetryAfterPath}, {"retryDelayPath", o.RetryDelayPath},
		{"windowNamePath", o.WindowNamePath}, {"windowLimitPath", o.WindowLimitPath},
		{"windowUsedPath", o.WindowUsedPath}, {"windowRemainingPath", o.WindowRemainingPath},
	} {
		if item.path != "" && !validJSONPointer(item.path) {
			return fmt.Errorf("error classifier %s must be a valid absolute JSON pointer", item.name)
		}
	}
	for _, values := range [][]string{o.QuotaCodes, o.QuotaTypes, o.QuotaStatuses, o.QuotaMessageTokens} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("error classifier match values must not be empty")
			}
		}
	}
	return nil
}

func validOutcomeScope(scope kernel.OutcomeScope) bool {
	switch scope {
	case kernel.ScopeRequest, kernel.ScopeRoute, kernel.ScopeRouteConnection, kernel.ScopeConnection, kernel.ScopeProvider:
		return true
	default:
		return false
	}
}

func validJSONPointer(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for index := 0; index < len(path); index++ {
		if path[index] != '~' {
			continue
		}
		if index+1 >= len(path) || (path[index+1] != '0' && path[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

type OperationBinding struct {
	Endpoint               PrimitiveRef              `json:"endpoint"`
	EndpointOptions        kernel.EndpointOptions    `json:"endpointOptions,omitempty"`
	Transport              PrimitiveRef              `json:"transport"`
	RequestCodec           PrimitiveRef              `json:"requestCodec,omitempty"`
	ResponseCodec          PrimitiveRef              `json:"responseCodec,omitempty"`
	ModelSource            PrimitiveRef              `json:"modelSource,omitempty"`
	UsageSource            PrimitiveRef              `json:"usageSource,omitempty"`
	UsageOptions           kernel.UsageSourceOptions `json:"usageOptions,omitempty"`
	QuotaSource            PrimitiveRef              `json:"quotaSource,omitempty"`
	QuotaWindowName        string                    `json:"quotaWindowName,omitempty"`
	ErrorClassifier        PrimitiveRef              `json:"errorClassifier,omitempty"`
	ErrorClassifierOptions ErrorClassifierOptions    `json:"errorClassifierOptions,omitempty"`
}

type CapabilitySet struct {
	Chat        bool `json:"chat,omitempty"`
	Responses   bool `json:"responses,omitempty"`
	Messages    bool `json:"messages,omitempty"`
	Embeddings  bool `json:"embeddings,omitempty"`
	Image       bool `json:"image,omitempty"`
	AudioInput  bool `json:"audioInput,omitempty"`
	AudioOutput bool `json:"audioOutput,omitempty"`
	Video       bool `json:"video,omitempty"`
	Tools       bool `json:"tools,omitempty"`
	Thinking    bool `json:"thinking,omitempty"`
	Streaming   bool `json:"streaming,omitempty"`
	Usage       bool `json:"usage,omitempty"`
	Quota       bool `json:"quota,omitempty"`
}

type OAuthFlowOptions struct {
	ClientID    string   `json:"clientId"`
	AuthURL     string   `json:"authUrl"`
	TokenURL    string   `json:"tokenUrl"`
	Scopes      []string `json:"scopes,omitempty"`
	RedirectURL string   `json:"redirectUrl,omitempty"`
}

type AuthOptions struct {
	OAuth *OAuthFlowOptions `json:"oauth,omitempty"`
}

type ProviderDefinition struct {
	ID           string                         `json:"id"`
	Version      string                         `json:"version"`
	DisplayName  string                         `json:"displayName"`
	Aliases      []string                       `json:"aliases,omitempty"`
	Operations   map[Operation]OperationBinding `json:"operations"`
	Auth         PrimitiveRef                   `json:"auth"`
	AuthOptions  AuthOptions                    `json:"authOptions,omitempty"`
	Session      PrimitiveRef                   `json:"session,omitempty"`
	Extensions   []PrimitiveRef                 `json:"extensions,omitempty"`
	Capabilities CapabilitySet                  `json:"capabilities"`
	Defaults     map[string]any                 `json:"defaults,omitempty"`
}

func DecodeProviderDefinitionJSON(data []byte) (ProviderDefinition, error) {
	var definition ProviderDefinition
	if err := decodeStrictJSON(data, &definition); err != nil {
		return ProviderDefinition{}, fmt.Errorf("decode provider definition: %w", err)
	}
	return definition, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func EncodeProviderDefinitionJSON(definition ProviderDefinition) ([]byte, error) {
	return json.MarshalIndent(definition, "", "  ")
}

type PrimitivePlugin interface {
	Definition() ProviderDefinition
}

type PrimitiveRegistry struct {
	definitions map[string]ProviderDefinition
	primitives  map[PrimitiveKind]map[string]struct{}
}

type RuntimeRegistry struct {
	Primitives       *PrimitiveRegistry
	Auth             *AuthRegistry
	endpoints        map[string]kernel.Endpoint
	transports       map[string]kernel.Transport
	requestCodecs    map[string]kernel.RequestCodec
	responseCodecs   map[string]kernel.ResponseCodec
	modelSources     map[string]ModelSource
	usageSources     map[string]UsageSource
	sessionStores    map[string]kernel.SessionStore
	errorClassifiers map[string]ErrorClassifier
	quotaSources     map[string]QuotaSource
}

func NewRuntimeRegistry() (*RuntimeRegistry, error) {
	primitives, err := NewBuiltinPrimitiveRegistry()
	if err != nil {
		return nil, err
	}
	runtime := &RuntimeRegistry{Primitives: primitives, Auth: NewAuthRegistry(), endpoints: map[string]kernel.Endpoint{}, transports: map[string]kernel.Transport{}, requestCodecs: map[string]kernel.RequestCodec{}, responseCodecs: map[string]kernel.ResponseCodec{}, modelSources: map[string]ModelSource{}, usageSources: map[string]UsageSource{}, sessionStores: map[string]kernel.SessionStore{}, errorClassifiers: map[string]ErrorClassifier{}, quotaSources: map[string]QuotaSource{}}
	requestCodec, responseCodec := openai.NewChatCodecs()
	registrations := []error{
		runtime.RegisterEndpoint(kernel.HTTPJSONEndpoint{}), runtime.RegisterTransport(kernel.HTTPTransport{}),
		runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseCodec(responseCodec),
	}
	requestCodec, responseCodec = openai.NewResponsesCodecs()
	registrations = append(registrations, runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseCodec(responseCodec))
	requestCodec, responseCodec = anthropic.NewMessageCodecs()
	registrations = append(registrations, runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseCodec(responseCodec))
	requestCodec, responseCodec = gemini.NewCodecs()
	registrations = append(registrations, runtime.RegisterRequestCodec(requestCodec), runtime.RegisterResponseCodec(responseCodec))
	registrations = append(registrations,
		runtime.RegisterSessionStore(kernel.NewMemorySessionStore()),
		runtime.RegisterModelSource(OpenAIModelSource{}), runtime.RegisterModelSource(AnthropicModelSource{}), runtime.RegisterModelSource(StaticModelSource{}),
		runtime.RegisterUsageSource(HTTPHeaderUsageSource{}),
		runtime.RegisterErrorClassifier(HTTPJSONErrorClassifier{}),
		runtime.RegisterQuotaSource(NoopQuotaSource{}), runtime.RegisterQuotaSource(HTTPJSONQuotaSource{}),
		runtime.RegisterAuthFactory("oauth2", OAuthAuthFactory),
	)
	for _, registrationErr := range registrations {
		if registrationErr != nil {
			return nil, registrationErr
		}
	}
	return runtime, nil
}

func (r *RuntimeRegistry) registerImplementation(kind PrimitiveKind, id string) error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("runtime registry is not initialized")
	}
	if id == "" {
		return fmt.Errorf("%s primitive ID is required", kind)
	}
	if r.Primitives.hasPrimitive(PrimitiveRef{Kind: PrimitiveKind(kind), ID: id}) {
		return nil
	}
	return r.Primitives.RegisterPrimitive(PrimitiveKind(kind), id)
}

func (r *RuntimeRegistry) ensureReady() error {
	if r == nil || r.Primitives == nil {
		return fmt.Errorf("runtime registry is not initialized")
	}
	return nil
}

func (r *RuntimeRegistry) RegisterEndpoint(endpoint kernel.Endpoint) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if endpoint == nil {
		return fmt.Errorf("endpoint implementation is nil")
	}
	if _, exists := r.endpoints[endpoint.ID()]; exists {
		return fmt.Errorf("endpoint %q is already registered", endpoint.ID())
	}
	if err := r.registerImplementation(PrimitiveEndpoint, endpoint.ID()); err != nil {
		return err
	}
	r.endpoints[endpoint.ID()] = endpoint
	return nil
}

func (r *RuntimeRegistry) RegisterTransport(transport kernel.Transport) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if transport == nil {
		return fmt.Errorf("transport implementation is nil")
	}
	if _, exists := r.transports[transport.ID()]; exists {
		return fmt.Errorf("transport %q is already registered", transport.ID())
	}
	if err := r.registerImplementation(PrimitiveTransport, transport.ID()); err != nil {
		return err
	}
	r.transports[transport.ID()] = transport
	return nil
}

func (r *RuntimeRegistry) RegisterRequestCodec(codec kernel.RequestCodec) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if codec == nil {
		return fmt.Errorf("request codec implementation is nil")
	}
	if _, exists := r.requestCodecs[codec.ID()]; exists {
		return fmt.Errorf("request codec %q is already registered", codec.ID())
	}
	if err := r.registerImplementation(PrimitiveRequestCodec, codec.ID()); err != nil {
		return err
	}
	r.requestCodecs[codec.ID()] = codec
	return nil
}

func (r *RuntimeRegistry) RegisterResponseCodec(codec kernel.ResponseCodec) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if codec == nil {
		return fmt.Errorf("response codec implementation is nil")
	}
	if _, exists := r.responseCodecs[codec.ID()]; exists {
		return fmt.Errorf("response codec %q is already registered", codec.ID())
	}
	if err := r.registerImplementation(PrimitiveResponseCodec, codec.ID()); err != nil {
		return err
	}
	r.responseCodecs[codec.ID()] = codec
	return nil
}

func (r *RuntimeRegistry) RegisterModelSource(source ModelSource) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("model source implementation is nil")
	}
	if _, exists := r.modelSources[source.ID()]; exists {
		return fmt.Errorf("model source %q is already registered", source.ID())
	}
	if err := r.registerImplementation(PrimitiveModelSource, source.ID()); err != nil {
		return err
	}
	r.modelSources[source.ID()] = source
	return nil
}

func (r *RuntimeRegistry) RegisterUsageSource(source UsageSource) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("usage source implementation is nil")
	}
	if _, exists := r.usageSources[source.ID()]; exists {
		return fmt.Errorf("usage source %q is already registered", source.ID())
	}
	if err := r.registerImplementation(PrimitiveUsageSource, source.ID()); err != nil {
		return err
	}
	r.usageSources[source.ID()] = source
	return nil
}

func (r *RuntimeRegistry) RegisterSessionStore(sessionStore kernel.SessionStore) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if sessionStore == nil {
		return fmt.Errorf("session store implementation is nil")
	}
	if _, exists := r.sessionStores[sessionStore.ID()]; exists {
		return fmt.Errorf("session store %q is already registered", sessionStore.ID())
	}
	if err := r.registerImplementation(PrimitiveSessionStore, sessionStore.ID()); err != nil {
		return err
	}
	r.sessionStores[sessionStore.ID()] = sessionStore
	return nil
}

// ReplaceSessionStore lets the daemon install its durable, async-backed
// implementation under the same manifest primitive before bindings are built.
func (r *RuntimeRegistry) ReplaceSessionStore(sessionStore kernel.SessionStore) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if sessionStore == nil {
		return fmt.Errorf("session store implementation is nil")
	}
	if _, exists := r.sessionStores[sessionStore.ID()]; !exists {
		return fmt.Errorf("session store %q is not registered", sessionStore.ID())
	}
	r.sessionStores[sessionStore.ID()] = sessionStore
	return nil
}

func (r *RuntimeRegistry) SessionStores() map[string]kernel.SessionStore {
	result := make(map[string]kernel.SessionStore, len(r.sessionStores))
	for id, sessionStore := range r.sessionStores {
		result[id] = sessionStore
	}
	return result
}

func (r *RuntimeRegistry) Endpoints() map[string]kernel.Endpoint {
	result := make(map[string]kernel.Endpoint, len(r.endpoints))
	for id, endpoint := range r.endpoints {
		result[id] = endpoint
	}
	return result
}

func (r *RuntimeRegistry) Transports() map[string]kernel.Transport {
	result := make(map[string]kernel.Transport, len(r.transports))
	for id, transport := range r.transports {
		result[id] = transport
	}
	return result
}

func (r *RuntimeRegistry) RegisterErrorClassifier(classifier ErrorClassifier) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if classifier == nil {
		return fmt.Errorf("error classifier implementation is nil")
	}
	if _, exists := r.errorClassifiers[classifier.ID()]; exists {
		return fmt.Errorf("error classifier %q is already registered", classifier.ID())
	}
	if err := r.registerImplementation(PrimitiveErrorClassifier, classifier.ID()); err != nil {
		return err
	}
	r.errorClassifiers[classifier.ID()] = classifier
	return nil
}

func (r *RuntimeRegistry) RegisterQuotaSource(source QuotaSource) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if source == nil {
		return fmt.Errorf("quota source implementation is nil")
	}
	if _, exists := r.quotaSources[source.ID()]; exists {
		return fmt.Errorf("quota source %q is already registered", source.ID())
	}
	if err := r.registerImplementation(PrimitiveQuotaSource, source.ID()); err != nil {
		return err
	}
	r.quotaSources[source.ID()] = source
	return nil
}

func (r *RuntimeRegistry) RegisterAuthFlow(flow AuthFlow) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if flow == nil {
		return fmt.Errorf("auth flow implementation is nil")
	}
	if err := r.registerImplementation(PrimitiveAuth, flow.ID()); err != nil {
		return err
	}
	return r.Auth.Register(flow)
}

func (r *RuntimeRegistry) RegisterAuthFactory(id string, factory AuthFlowFactory) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if err := r.registerImplementation(PrimitiveAuth, id); err != nil {
		return err
	}
	return r.Auth.RegisterFactory(id, factory)
}

func (r *RuntimeRegistry) AdaptersForBindings(bindings map[string]RuntimeBinding) map[string]kernel.ProviderAdapter {
	result := make(map[string]kernel.ProviderAdapter, len(bindings))
	for _, binding := range bindings {
		if binding.Adapter != nil {
			result[binding.AdapterID] = binding.Adapter
		}
	}
	return result
}

func (r *RuntimeRegistry) AuthFlowsForBindings(bindings map[string]RuntimeBinding) map[string]AuthFlow {
	result := make(map[string]AuthFlow, len(bindings))
	for _, binding := range bindings {
		if binding.Auth != nil {
			result[binding.AuthFlowID] = binding.Auth
		}
	}
	return result
}

func (r *RuntimeRegistry) DefaultCredentialTypes() map[string]string {
	result := make(map[string]string)
	if r == nil || r.Primitives == nil {
		return result
	}
	for _, definition := range r.Primitives.Definitions() {
		if definition.Auth.ID == "" {
			continue
		}
		credentialType := definition.Auth.ID
		if credentialType == "static-secret" {
			credentialType = "api_key"
		}
		result[definition.ID] = credentialType
	}
	return result
}

func (r *RuntimeRegistry) AuthFlowIDsByDefinition() map[string]string {
	result := make(map[string]string)
	if r == nil || r.Primitives == nil {
		return result
	}
	for _, definition := range r.Primitives.Definitions() {
		if definition.Auth.ID != "" {
			result[definition.ID] = AuthBindingKey(definition.ID, definition.Auth.ID)
		}
	}
	return result
}

func (r *RuntimeRegistry) ErrorClassifiers() map[string]kernel.ErrorClassifier {
	result := make(map[string]kernel.ErrorClassifier, len(r.errorClassifiers))
	for id, classifier := range r.errorClassifiers {
		result[id] = adaptErrorClassifier(classifier)
	}
	return result
}

func (r *RuntimeRegistry) ErrorClassifiersForBindings(bindings map[string]RuntimeBinding) map[string]kernel.ErrorClassifier {
	result := r.ErrorClassifiers()
	for _, binding := range bindings {
		if binding.ErrorClassifier == nil || binding.ErrorClassifierID == "" || binding.ErrorClassifierID == binding.ErrorClassifier.ID() {
			continue
		}
		result[binding.ErrorClassifierID] = adaptErrorClassifier(binding.ErrorClassifier)
	}
	return result
}

func adaptErrorClassifier(classifier ErrorClassifier) kernel.ErrorClassifier {
	adapter := classifierAdapter{classifier}
	if rich, ok := classifier.(kernel.OutcomeClassifier); ok {
		return outcomeClassifierAdapter{classifierAdapter: adapter, outcomeClassifier: rich}
	}
	return adapter
}

func (r *RuntimeRegistry) QuotaSources() map[string]QuotaSource {
	result := make(map[string]QuotaSource, len(r.quotaSources))
	for id, source := range r.quotaSources {
		result[id] = source
	}
	return result
}

func (r *RuntimeRegistry) UsageSources() map[string]kernel.UsageEnricher {
	result := make(map[string]kernel.UsageEnricher, len(r.usageSources))
	for id, source := range r.usageSources {
		result[id] = source
	}
	return result
}

type classifierAdapter struct{ classifier ErrorClassifier }

func (c classifierAdapter) ClassifyError(status int, body []byte) kernel.ErrorClass {
	return c.classifier.Classify(status, body)
}

type outcomeClassifierAdapter struct {
	classifierAdapter
	outcomeClassifier kernel.OutcomeClassifier
}

func (c outcomeClassifierAdapter) ClassifyOutcome(status int, headers http.Header, body []byte) kernel.ClassifiedOutcome {
	return c.outcomeClassifier.ClassifyOutcome(status, headers, body)
}

func NewPrimitiveRegistry() *PrimitiveRegistry {
	return &PrimitiveRegistry{
		definitions: map[string]ProviderDefinition{},
		primitives: map[PrimitiveKind]map[string]struct{}{
			PrimitiveEndpoint: {}, PrimitiveTransport: {}, PrimitiveAuth: {}, PrimitiveRequestCodec: {},
			PrimitiveResponseCodec: {}, PrimitiveModelSource: {}, PrimitiveUsageSource: {},
			PrimitiveQuotaSource: {}, PrimitiveSessionStore: {},
			PrimitiveErrorClassifier: {}, PrimitiveExtension: {},
		},
	}
}

func (r *PrimitiveRegistry) RegisterPrimitive(kind PrimitiveKind, id string) error {
	if r == nil {
		return fmt.Errorf("primitive registry is nil")
	}
	if id == "" {
		return fmt.Errorf("primitive ID is required")
	}
	set, ok := r.primitives[kind]
	if !ok {
		return fmt.Errorf("unknown primitive kind %q", kind)
	}
	if _, exists := set[id]; exists {
		return fmt.Errorf("primitive %s/%q already registered", kind, id)
	}
	set[id] = struct{}{}
	return nil
}

func (r *PrimitiveRegistry) RegisterDefinition(def ProviderDefinition) error {
	if r == nil {
		return fmt.Errorf("primitive registry is nil")
	}
	if def.ID == "" || def.Version == "" || def.DisplayName == "" {
		return fmt.Errorf("provider definition requires ID, version and display name")
	}
	if _, exists := r.definitions[def.ID]; exists {
		return fmt.Errorf("provider definition %q already registered", def.ID)
	}
	if len(def.Operations) == 0 {
		return fmt.Errorf("provider %q has no operations", def.ID)
	}
	if def.Auth.ID == "" {
		return fmt.Errorf("provider %q must bind an auth primitive explicitly (use %q when no credential is needed)", def.ID, "none")
	}
	if !r.hasPrimitive(def.Auth) {
		return fmt.Errorf("provider %q references unknown auth primitive %q", def.ID, def.Auth.ID)
	}
	if def.Session.ID != "" && !r.hasPrimitive(def.Session) {
		return fmt.Errorf("provider %q references unknown session store %q", def.ID, def.Session.ID)
	}
	for operation, binding := range def.Operations {
		if operation == "" || binding.Endpoint.ID == "" || binding.Transport.ID == "" {
			return fmt.Errorf("provider %q has incomplete %q operation binding", def.ID, operation)
		}
		requestResponseOperation := operation == OperationChat || operation == OperationResponses || operation == OperationMessages
		if requestResponseOperation && (binding.RequestCodec.ID == "" || binding.ResponseCodec.ID == "") {
			return fmt.Errorf("provider %q has incomplete %q codec binding", def.ID, operation)
		}
		if operation == OperationQuota && binding.QuotaSource.ID == "" {
			return fmt.Errorf("provider %q quota operation requires a quotaSource", def.ID)
		}
		if operation != OperationQuota && binding.QuotaSource.ID != "" {
			return fmt.Errorf("provider %q quotaSource must be bound to the quota operation", def.ID)
		}
		if !binding.ErrorClassifierOptions.Empty() {
			if binding.ErrorClassifier.ID == "" {
				return fmt.Errorf("provider %q %q classifier options require an errorClassifier", def.ID, operation)
			}
			if binding.ErrorClassifierOptions.HTTPJSON == nil || binding.ErrorClassifier.ID != "http-json" {
				return fmt.Errorf("provider %q %q has options unsupported by classifier %q", def.ID, operation, binding.ErrorClassifier.ID)
			}
			if err := binding.ErrorClassifierOptions.HTTPJSON.Validate(); err != nil {
				return fmt.Errorf("provider %q %q: %w", def.ID, operation, err)
			}
		}
		for _, ref := range []PrimitiveRef{binding.Endpoint, binding.Transport, binding.RequestCodec, binding.ResponseCodec, binding.ModelSource, binding.UsageSource, binding.QuotaSource, binding.ErrorClassifier} {
			if ref.ID != "" && !r.hasPrimitive(ref) {
				return fmt.Errorf("provider %q references unknown %s primitive %q", def.ID, ref.Kind, ref.ID)
			}
		}
	}
	for _, extension := range def.Extensions {
		if extension.ID == "" || !r.hasPrimitive(extension) {
			return fmt.Errorf("provider %q references unknown extension %q", def.ID, extension.ID)
		}
	}
	r.definitions[def.ID] = def
	return nil
}

func (r *PrimitiveRegistry) RegisterPlugin(plugin PrimitivePlugin) error {
	if plugin == nil {
		return fmt.Errorf("provider plugin is nil")
	}
	return r.RegisterDefinition(plugin.Definition())
}

func (r *PrimitiveRegistry) Definition(id string) (ProviderDefinition, bool) {
	definition, ok := r.definitions[id]
	return definition, ok
}

func (r *PrimitiveRegistry) Definitions() []ProviderDefinition {
	result := make([]ProviderDefinition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		result = append(result, definition)
	}
	return result
}

func (r *PrimitiveRegistry) hasPrimitive(ref PrimitiveRef) bool {
	if ref.ID == "" {
		return false
	}
	set, ok := r.primitives[ref.Kind]
	if !ok {
		return false
	}
	_, exists := set[ref.ID]
	return exists
}
