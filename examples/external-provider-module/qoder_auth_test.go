package externalprovider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
	"github.com/google/uuid"
)

// These constants and byte-order rules follow the adjacent 9router
// open-sse/shared/qoder/{constants,encoding,cosy}.js implementation. The test
// signs only a fixture payload and sends it to httptest; it never contacts Qoder.
const (
	qoderFixturePath           = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	qoderFixtureStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderFixtureCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	qoderFixturePublicKey      = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`
)

type qoderFixtureAuth struct{}

func (qoderFixtureAuth) ID() string { return "example.qoder-auth" }
func (qoderFixtureAuth) SetupSchema() provider.SetupSchema {
	return provider.SetupSchema{Fields: []provider.SetupField{{Name: "credential", Type: "string", Required: true, Secret: true}}}
}
func (qoderFixtureAuth) Resolve(_ context.Context, input provider.AuthInput) (provider.Credential, error) {
	var state struct {
		AccessToken string `json:"access_token"`
		UserID      string `json:"user_id"`
		MachineID   string `json:"machine_id"`
		Name        string `json:"name"`
		Email       string `json:"email"`
	}
	if err := json.Unmarshal([]byte(input.Secret), &state); err != nil {
		return provider.Credential{}, fmt.Errorf("decode Qoder fixture credential: %w", err)
	}
	if state.AccessToken == "" || state.UserID == "" || state.MachineID == "" {
		return provider.Credential{}, fmt.Errorf("Qoder fixture credential is incomplete")
	}
	return provider.Credential{
		ConnectionID: input.ConnectionID, Type: input.Type, Secret: state.AccessToken,
		AuthContext: provider.AuthContext{"userId": state.UserID, "machineId": state.MachineID, "name": state.Name, "email": state.Email},
	}, nil
}
func (qoderFixtureAuth) Refresh(_ context.Context, credential provider.Credential) (provider.Credential, error) {
	return credential, nil
}

type qoderFixtureModule struct{}

func (qoderFixtureModule) ID() string { return "example.qoder-signing-fixture" }
func (qoderFixtureModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterAuthFactory("example.qoder-auth", func(provider.AuthOptions) (provider.AuthFlow, error) {
		return qoderFixtureAuth{}, nil
	}); err != nil {
		return err
	}
	if err := runtime.RegisterRequestCodec(qoderFixtureRequestCodec{}); err != nil {
		return err
	}
	return runtime.RegisterResponseDecoder(qoderFixtureResponseDecoder{})
}

type qoderFixtureRequestCodec struct{}

func (qoderFixtureRequestCodec) ID() string { return "example.qoder-cosy-request" }
func (qoderFixtureRequestCodec) DescribeCompatibility(provider.CompatibilityContext) []provider.FacetMapping {
	return []provider.FacetMapping{{Facet: provider.FacetWireRequest, Paths: []string{"qoderFixturePayload"}, Disposition: provider.FacetTranslated}}
}
func (qoderFixtureRequestCodec) Prepare(_ context.Context, request provider.NormalizedRequest, route provider.Route, credential provider.Credential) (provider.UpstreamRequest, error) {
	if err := credential.ValidateRuntimeContext(); err != nil {
		return provider.UpstreamRequest{}, err
	}
	userID := credential.AuthContext["userId"]
	if userID == "" || credential.Secret == "" {
		return provider.UpstreamRequest{}, fmt.Errorf("Qoder fixture requires auth context and access token")
	}
	payload, err := json.Marshal(map[string]any{
		"model":    route.ExternalModel,
		"messages": request.Raw["messages"],
		"stream":   request.Stream,
	})
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	encodedBody := qoderFixtureEncodeBody(payload)
	absoluteURL := strings.TrimRight(route.BaseURL, "/") + qoderFixturePath
	headers, err := qoderFixtureCOSYHeaders(encodedBody, absoluteURL, credential)
	if err != nil {
		return provider.UpstreamRequest{}, err
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Accept-Encoding", "identity")
	headers.Set("X-Model-Key", route.ExternalModel)
	headers.Set("X-Model-Source", "system")
	return provider.UpstreamRequest{Method: http.MethodPost, URL: qoderFixturePath, Headers: headers, Body: bytes.NewReader(encodedBody)}, nil
}

type qoderFixtureResponseDecoder struct{}

func (qoderFixtureResponseDecoder) ID() string { return "example.qoder-envelope-sse" }
func (qoderFixtureResponseDecoder) PossibleEvents() []provider.ResponseEventKind {
	return []provider.ResponseEventKind{provider.EventTextDelta, provider.EventResponseComplete}
}
func (qoderFixtureResponseDecoder) ClassifyError(status int, _ []byte) provider.ErrorClass {
	if status >= http.StatusBadRequest {
		return "terminal"
	}
	return "retryable"
}
func (qoderFixtureResponseDecoder) Decode(_ context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, _ provider.StreamHooks) error {
	if response.Body == nil {
		return fmt.Errorf("Qoder fixture response body is required")
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	completed := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var envelope struct {
			StatusCodeValue int    `json:"statusCodeValue"`
			Body            string `json:"body"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &envelope); err != nil {
			return fmt.Errorf("decode Qoder fixture SSE envelope: %w", err)
		}
		if envelope.StatusCodeValue != 0 && envelope.StatusCodeValue != http.StatusOK {
			return fmt.Errorf("Qoder fixture envelope reports status %d", envelope.StatusCodeValue)
		}
		if envelope.Body == "[DONE]" {
			if !completed {
				if err := emit(provider.ResponseEvent{Kind: provider.EventResponseComplete}); err != nil {
					return err
				}
				completed = true
			}
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(envelope.Body), &chunk); err != nil {
			return fmt.Errorf("decode Qoder fixture inner chunk: %w", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if err := emit(provider.ResponseEvent{Kind: provider.EventTextDelta, Text: choice.Delta.Content}); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !completed {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func TestExternalRequestCodecEncodesThenSignsBodyWithoutKernelChanges(t *testing.T) {
	var received bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = true
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read signed request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.URL.Path != "/algo/api/v2/service/pro/sse/agent_chat_generation" || request.URL.Query().Get("Encode") != "1" {
			t.Errorf("Qoder fixture URL=%s", request.URL)
		}
		if request.Header.Get("Cosy-Bodyhash") != qoderFixtureMD5(body) || request.Header.Get("Cosy-Bodylength") != fmt.Sprint(len(body)) || request.Header.Get("Cosy-Sigpath") != "/api/v2/service/pro/sse/agent_chat_generation" {
			t.Errorf("body-bound auth headers do not describe sent bytes: headers=%#v len=%d", request.Header, len(body))
		}
		encodedAuthorization := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer COSY.")
		parts := strings.Split(encodedAuthorization, ".")
		if len(parts) != 2 {
			t.Errorf("COSY Authorization shape is invalid: %q", request.Header.Get("Authorization"))
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		signatureInput := parts[0] + "\n" + request.Header.Get("Cosy-Key") + "\n" + request.Header.Get("Cosy-Date") + "\n" + string(body) + "\n" + request.Header.Get("Cosy-Sigpath")
		if parts[1] != qoderFixtureMD5([]byte(signatureInput)) {
			t.Errorf("signature does not cover the exact encoded request body")
		}
		if wrapped, err := base64.StdEncoding.DecodeString(request.Header.Get("Cosy-Key")); err != nil || len(wrapped) != 128 {
			t.Errorf("RSA-wrapped COSY key invalid: decoded_len=%d err=%v", len(wrapped), err)
		}
		decoded, err := qoderFixtureDecodeBody(body)
		if err != nil {
			t.Errorf("decode Qoder fixture body: %v", err)
		} else {
			var payload map[string]any
			if err := json.Unmarshal(decoded, &payload); err != nil || payload["model"] != "qmodel_latest" || payload["stream"] != true {
				t.Errorf("encoded payload=%s err=%v", decoded, err)
			}
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer,
			"data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"wrapped result\\\"}}]}\"}\n\n"+
				"data: {\"statusCodeValue\":200,\"body\":\"[DONE]\"}\n\n")
	}))
	defer server.Close()

	runtime, err := provider.NewRuntimeRegistryWithModules(qoderFixtureModule{})
	if err != nil {
		t.Fatal(err)
	}
	definition := provider.ProviderDefinition{
		ContractVersion: 1, ID: "qoder-cosy-fixture", Version: "1", DisplayName: "Qoder COSY fixture",
		DefaultCredentialType: "qoder_oauth",
		AuthModes:             []provider.AuthModeDefinition{{CredentialType: "qoder_oauth", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "example.qoder-auth", ContractVersion: 1}}},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {
			Protocol: "openai_chat", TaskRef: &provider.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 1}, ProviderFormat: "qoder-cosy",
			Endpoint:        provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "http-json", ContractVersion: 1},
			Transport:       provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1},
			RequestCodec:    provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: "example.qoder-cosy-request", ContractVersion: 1},
			ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "example.qoder-envelope-sse", ContractVersion: 1},
		}},
	}
	if err := runtime.Primitives.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	bindings, err := runtime.BuildBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding := bindings[provider.RuntimeBindingKey(definition.ID, provider.OperationChat)]
	flow := binding.AuthModes["qoder_oauth"].Flow
	credential, err := flow.Resolve(context.Background(), provider.AuthInput{
		ConnectionID: "qoder-account", Type: "qoder_oauth",
		Secret: `{"access_token":"device-token","user_id":"user-42","machine_id":"machine-42","name":"Fixture","email":"fixture@example.test"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := provider.NormalizedRequest{Model: "qoder/qmodel_latest", Stream: true, Raw: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}}}
	prepared, err := binding.Adapter.Prepare(context.Background(), request, provider.Route{BaseURL: server.URL, ExternalModel: "qmodel_latest"}, credential)
	if err != nil {
		t.Fatal(err)
	}
	response, err := binding.Adapter.Execute(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	client := httptest.NewRecorder()
	if err := binding.Adapter.RenderResponse(context.Background(), response, client, provider.Format("openai"), provider.StreamHooks{Streaming: true}); err != nil {
		t.Fatal(err)
	}
	if !received || response.Status != http.StatusOK || !strings.Contains(client.Body.String(), "wrapped result") {
		t.Fatalf("signed request/response fixture failed: received=%v response=%#v client_status=%d client_headers=%#v client_body=%s", received, response, client.Code, client.Header(), client.Body.String())
	}
}

func qoderFixtureEncodeBody(plain []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(plain)
	n := len(encoded)
	third := n / 3
	reordered := encoded[n-third:] + encoded[third:n-third] + encoded[:third]
	custom := make([]byte, len(reordered))
	for index, value := range []byte(reordered) {
		if value == '=' {
			custom[index] = '$'
			continue
		}
		alphabetIndex := strings.IndexByte(qoderFixtureStdAlphabet, value)
		if alphabetIndex < 0 {
			custom[index] = value
			continue
		}
		custom[index] = qoderFixtureCustomAlphabet[alphabetIndex]
	}
	return custom
}

func qoderFixtureDecodeBody(encoded []byte) ([]byte, error) {
	standard := make([]byte, len(encoded))
	for index, value := range encoded {
		if value == '$' {
			standard[index] = '='
			continue
		}
		alphabetIndex := strings.IndexByte(qoderFixtureCustomAlphabet, value)
		if alphabetIndex < 0 {
			standard[index] = value
			continue
		}
		standard[index] = qoderFixtureStdAlphabet[alphabetIndex]
	}
	reordered := string(standard)
	n := len(reordered)
	third := n / 3
	base64Body := reordered[n-third:] + reordered[third:n-third] + reordered[:third]
	return base64.StdEncoding.DecodeString(base64Body)
}

func qoderFixtureCOSYHeaders(body []byte, requestURL string, credential provider.Credential) (http.Header, error) {
	userID, token := credential.AuthContext["userId"], credential.Secret
	if userID == "" || token == "" {
		return nil, fmt.Errorf("COSY fixture requires user ID and access token")
	}
	aesKey := uuid.NewString()[:16]
	userinfo, err := json.Marshal(struct {
		UserID    string `json:"uid"`
		Token     string `json:"security_oauth_token"`
		Name      string `json:"name"`
		AccountID string `json:"aid"`
		Email     string `json:"email"`
	}{userID, token, credential.AuthContext["name"], "", credential.AuthContext["email"]})
	if err != nil {
		return nil, err
	}
	info, err := qoderFixtureEncryptAES(userinfo, []byte(aesKey))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(qoderFixturePublicKey))
	if block == nil {
		return nil, fmt.Errorf("decode COSY public key: PEM block missing")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("COSY public key is not RSA")
	}
	wrappedKey, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, []byte(aesKey))
	if err != nil {
		return nil, err
	}
	requestID := uuid.NewString()
	envelope, err := json.Marshal(struct {
		Version     string `json:"version"`
		RequestID   string `json:"requestId"`
		Info        string `json:"info"`
		COSYVersion string `json:"cosyVersion"`
		IDEVersion  string `json:"ideVersion"`
	}{"v1", requestID, info, "1.0.0", ""})
	if err != nil {
		return nil, err
	}
	payloadB64 := base64.StdEncoding.EncodeToString(envelope)
	cosyKey := base64.StdEncoding.EncodeToString(wrappedKey)
	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		return nil, err
	}
	sigPath := strings.TrimPrefix(parsedURL.Path, "/algo")
	timestamp := fmt.Sprint(time.Now().Unix())
	signatureInput := payloadB64 + "\n" + cosyKey + "\n" + timestamp + "\n" + string(body) + "\n" + sigPath
	signature := qoderFixtureMD5([]byte(signatureInput))
	machineID := credential.AuthContext["machineId"]
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer COSY."+payloadB64+"."+signature)
	headers.Set("Cosy-Key", cosyKey)
	headers.Set("Cosy-User", userID)
	headers.Set("Cosy-Date", timestamp)
	headers.Set("Cosy-Version", "1.0.0")
	headers.Set("Cosy-Machineid", machineID)
	headers.Set("Cosy-Machinetoken", machineID)
	headers.Set("Cosy-Machinetype", "5")
	headers.Set("Cosy-Machineos", "x86_64_windows")
	headers.Set("Cosy-Clienttype", "5")
	headers.Set("Cosy-Clientip", "127.0.0.1")
	headers.Set("Cosy-Bodyhash", qoderFixtureMD5(body))
	headers.Set("Cosy-Bodylength", fmt.Sprint(len(body)))
	headers.Set("Cosy-Sigpath", sigPath)
	headers.Set("Cosy-Data-Policy", "disagree")
	headers.Set("Cosy-Organization-Id", "")
	headers.Set("Cosy-Organization-Tags", "")
	headers.Set("Login-Version", "v2")
	headers.Set("X-Request-Id", uuid.NewString())
	return headers, nil
}

func qoderFixtureEncryptAES(plain, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padding := aes.BlockSize - (len(plain) % aes.BlockSize)
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(encrypted, padded)
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func qoderFixtureMD5(input []byte) string {
	digest := md5.Sum(input)
	return hex.EncodeToString(digest[:])
}
