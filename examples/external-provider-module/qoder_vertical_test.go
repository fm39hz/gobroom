package externalprovider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/daemon"
	"github.com/fm39hz/gobroom/pkg/provider"
)

const qoderNativeCodecID = "example.qoder-native-request"
const qoderNativeProviderID = "example-qoder-native"

func qoderNativeDefinition(t *testing.T) provider.ProviderDefinition {
	t.Helper()
	options := json.RawMessage(`{"modelConfigs":{"qmodel_latest":{"key":"qmodel_latest","display_name":"Qoder selected","enable":true,"is_reasoning":false,"max_input_tokens":131072,"max_output_tokens":8192}}}`)
	return provider.ProviderDefinition{ContractVersion: 1, ID: qoderNativeProviderID, Version: "1", DisplayName: "Source-backed Qoder text fixture", DefaultCredentialType: "qoder_oauth",
		AuthModes: []provider.AuthModeDefinition{{CredentialType: "qoder_oauth", Auth: provider.PrimitiveRef{Kind: provider.PrimitiveAuth, ID: "example.qoder-auth", ContractVersion: 1}}},
		Operations: map[provider.Operation]provider.OperationBinding{provider.OperationChat: {Protocol: "qoder-native", TaskRef: &provider.Ref{Kind: "operation", ID: "chat.generate", ContractVersion: 1}, ProviderFormat: "qoder-native",
			Endpoint: provider.PrimitiveRef{Kind: provider.PrimitiveEndpoint, ID: "example.qoder-native-endpoint", ContractVersion: 1}, Transport: provider.PrimitiveRef{Kind: provider.PrimitiveTransport, ID: "http", ContractVersion: 1}, RequestCodec: provider.PrimitiveRef{Kind: provider.PrimitiveRequestCodec, ID: qoderNativeCodecID, ContractVersion: 1}, RequestCodecOptions: options, ResponseDecoder: provider.PrimitiveRef{Kind: provider.PrimitiveResponseDecoder, ID: "example.qoder-native-envelope", ContractVersion: 1}}}}
}

type qoderNativeFixture struct {
	host  *portabilityHost
	calls atomic.Int32
	mode  atomic.Int32
}

func newQoderNativeFixture(t *testing.T) *qoderNativeFixture {
	t.Helper()
	f := &qoderNativeFixture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.URL.Path != "/algo/api/v2/service/pro/sse/agent_chat_generation" || r.URL.Query().Get("FetchKeys") != "llm_model_result" || r.URL.Query().Get("AgentId") != "agent_common" || r.URL.Query().Get("Encode") != "1" {
			t.Error("native target/query changed")
			w.WriteHeader(400)
			return
		}
		digest := md5.Sum(body)
		if r.Header.Get("Cosy-Bodyhash") != hex.EncodeToString(digest[:]) || r.Header.Get("Cosy-Bodylength") != strconv.Itoa(len(body)) || r.Header.Get("Cosy-Sigpath") != "/api/v2/service/pro/sse/agent_chat_generation" || r.Header.Get("Cosy-User") != "native-user" || r.Header.Get("Cosy-Machineid") != "native-machine" {
			t.Error("signature does not cover exact dispatched bytes/scope")
			w.WriteHeader(400)
			return
		}
		encoded, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer COSY.")
		if !found {
			t.Error("COSY auth absent")
			w.WriteHeader(401)
			return
		}
		envelope, signature, ok := strings.Cut(encoded, ".")
		if !ok {
			t.Error("COSY auth malformed")
			w.WriteHeader(401)
			return
		}
		expectedSignature := md5.Sum([]byte(envelope + "\n" + r.Header.Get("Cosy-Key") + "\n" + r.Header.Get("Cosy-Date") + "\n" + string(body) + "\n/api/v2/service/pro/sse/agent_chat_generation"))
		if signature != hex.EncodeToString(expectedSignature[:]) {
			t.Error("signature input bytes differ")
			w.WriteHeader(401)
			return
		}
		key, err := base64.StdEncoding.DecodeString(r.Header.Get("Cosy-Key"))
		if err != nil || len(key) != 128 {
			t.Error("wrapped RSA key absent")
			w.WriteHeader(400)
			return
		}
		plain, err := qoderFixtureDecodeBody(body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var payload struct {
			RequestID   string                           `json:"request_id"`
			RequestSet  string                           `json:"request_set_id"`
			Record      string                           `json:"chat_record_id"`
			Session     string                           `json:"session_id"`
			Stream      bool                             `json:"stream"`
			Task        string                           `json:"chat_task"`
			Reply       bool                             `json:"is_reply"`
			Retry       bool                             `json:"is_retry"`
			Source      int                              `json:"source"`
			Version     string                           `json:"version"`
			SessionType string                           `json:"session_type"`
			Agent       string                           `json:"agent_id"`
			TaskID      string                           `json:"task_id"`
			System      string                           `json:"system"`
			Messages    []struct{ Role, Content string } `json:"messages"`
			Parameters  struct {
				MaxTokens int `json:"max_tokens"`
			} `json:"parameters"`
			ModelConfig struct {
				Key       string `json:"key"`
				MaxOutput int    `json:"max_output_tokens"`
			} `json:"model_config"`
			Context struct {
				Text  string `json:"text"`
				Extra struct {
					Original string `json:"originalContent"`
					Config   struct {
						Key       string `json:"key"`
						Reasoning bool   `json:"is_reasoning"`
					} `json:"modelConfig"`
				} `json:"extra"`
			} `json:"chat_context"`
			Business struct {
				Product, Version, Type, Stage, ID, Name string
				BeginAt                                 int64 `json:"begin_at"`
			} `json:"business"`
		}
		if err := json.Unmarshal(plain, &payload); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if payload.RequestID == "" || payload.RequestSet != payload.Record || len(payload.Record) != 16 || len(payload.Session) != 16 || !payload.Stream || payload.Task != "FREE_INPUT" || !payload.Reply || payload.Retry || payload.Source != 1 || payload.Version != "3" || payload.SessionType != "qodercli" || payload.Agent != "agent_common" || payload.TaskID != "common" || payload.System != "native system" || len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "native hello" || payload.Parameters.MaxTokens != 64 || payload.ModelConfig.Key != "qmodel_latest" || payload.ModelConfig.MaxOutput != 8192 || payload.Context.Text != "native hello" || payload.Context.Extra.Original != "native hello" || payload.Context.Extra.Config.Key != "qmodel_latest" || payload.Business.Product != "cli" || payload.Business.Type != "agent" || payload.Business.Stage != "start" || payload.Business.ID == "" || payload.Business.BeginAt <= 0 {
			t.Errorf("source payload structure wrong: %s", plain)
			w.WriteHeader(400)
			return
		}
		if _, err := hex.DecodeString(payload.Record); err != nil {
			t.Error("record id not hexadecimal")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if f.mode.Load() == 1 {
			_, _ = io.WriteString(w, "data: {\"statusCodeValue\":429,\"body\":\"private-native-quota\"}\n\n")
			return
		}
		inner := json.RawMessage(`{"id":"native-provider","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"native result"},"finish_reason":null}]}`)
		frame, _ := json.Marshal(struct {
			Status int             `json:"statusCodeValue"`
			Body   json.RawMessage `json:"body"`
		}{200, inner})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		if f.mode.Load() == 2 {
			return
		}
		final := `{"id":"native-provider","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
		frame, _ = json.Marshal(struct {
			Status int    `json:"statusCodeValue"`
			Body   string `json:"body"`
		}{200, final})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		frame, _ = json.Marshal(struct {
			Status int    `json:"statusCodeValue"`
			Body   string `json:"body"`
		}{200, "[DONE]"})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
	}))
	t.Cleanup(upstream.Close)
	root, err := os.MkdirTemp("", "gobroom-qoder-vertical-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manifestDir := filepath.Join(root, "providers")
	if err := os.Mkdir(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.EncodeManifest(qoderNativeDefinition(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "qoder.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{DBPath: filepath.Join(root, "state.db"), IPCPath: filepath.Join(root, "daemon.sock"), ProviderManifestDir: manifestDir, HTTPEnabled: true, HTTPAddr: "127.0.0.1:0", HTTPToken: "client-key-4b7e"}
	module := qoderNativeModule{}
	f.host = &portabilityHost{config: config, modules: []provider.RuntimeModule{module}, service: daemon.NewWithProviderModules(config, module)}
	if err := f.host.service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.host.service.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	create := func(method string, params map[string]any) string {
		var row struct{ ID string }
		if err := json.Unmarshal(portabilityOK(t, config.IPCPath, method, params), &row); err != nil || row.ID == "" {
			t.Fatalf("%s: %#v %v", method, row, err)
		}
		return row.ID
	}
	node := create("providers.create", map[string]any{"name": "Qoder native fixture", "prefix": "qnative", "baseUrl": upstream.URL, "protocol": "qoder-native", "definitionID": qoderNativeProviderID})
	connection := create("connections.create", map[string]any{"providerNodeID": node, "name": "Imported device credential", "credentialType": "qoder_oauth", "secret": `{"access_token":"native-private-token","user_id":"native-user","machine_id":"native-machine","name":"Native fixture"}`})
	portabilityOK(t, config.IPCPath, "custom_models.upsert", map[string]any{"id": "qoder-native-route", "providerNodeID": node, "kind": "custom", "externalID": "qmodel_latest", "displayName": "Selected Qoder model", "connectionIds": []string{connection}})
	portabilityOK(t, config.IPCPath, "physical_models.upsert", map[string]any{"name": "qoder-native-physical", "identity": map[string]any{"canonicalName": "selected-native-model"}, "sources": []map[string]any{{"routeId": "qoder-native-route", "fidelity": "exact"}}, "enabled": true})
	portabilityOK(t, config.IPCPath, "combo_models.upsert", map[string]any{"name": "qoder-native-role", "members": []map[string]any{{"kind": "physical", "id": "qoder-native-physical"}}, "discoverable": true, "enabled": true})
	return f
}

func (f *qoderNativeFixture) call(t *testing.T) (int, []byte) {
	t.Helper()
	return sg1Post(t, f.host, "/v1/chat/completions", json.RawMessage(`{"model":"qoder-native-role","messages":[{"role":"system","content":"native system"},{"role":"user","content":"native hello"}],"max_tokens":64}`), "")
}

func TestQoderNativePublicVerticalAndRestart(t *testing.T) {
	f := newQoderNativeFixture(t)
	var catalog []struct {
		ID        string `json:"id"`
		AuthModes []struct {
			CredentialType string               `json:"credentialType"`
			Setup          provider.SetupSchema `json:"setupSchema"`
		} `json:"authModes"`
	}
	if err := json.Unmarshal(portabilityOK(t, f.host.config.IPCPath, "providers.catalog", nil), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range catalog {
		if definition.ID == qoderNativeProviderID {
			for _, mode := range definition.AuthModes {
				for _, field := range mode.Setup.Fields {
					if mode.CredentialType == "qoder_oauth" && field.Name == "credential" && field.Required && field.Secret {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("normal setup catalog omitted secret credential schema")
	}
	for pass := range 2 {
		if pass > 0 {
			f.host.restart(t)
		}
		status, body := f.call(t)
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				Input  int `json:"prompt_tokens"`
				Output int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &result); err != nil || status != 200 || len(result.Choices) != 1 || result.Choices[0].Message.Content != "native result" || result.Usage.Input != 5 || result.Usage.Output != 3 {
			t.Fatalf("native JSON consume failed: HTTP%d body=%s err=%v", status, body, err)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatal("unexpected provider dispatches")
	}
}

func TestQoderNativePublicStreaming(t *testing.T) {
	f := newQoderNativeFixture(t)
	status, body := sg1Post(t, f.host, "/v1/chat/completions", json.RawMessage(`{"model":"qoder-native-role","messages":[{"role":"system","content":"native system"},{"role":"user","content":"native hello"}],"max_tokens":64,"stream":true,"stream_options":{"include_usage":true}}`), "")
	if status != 200 {
		t.Fatalf("native SSE consume failed: HTTP%d body=%s", status, body)
	}
	var text string
	done := 0
	usage := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			done++
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Input  int `json:"prompt_tokens"`
				Output int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			text += choice.Delta.Content
		}
		if chunk.Usage != nil && chunk.Usage.Input == 5 && chunk.Usage.Output == 3 {
			usage = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if text != "native result" || done != 1 || !usage || f.calls.Load() != 1 || bytes.Contains(body, []byte("statusCodeValue")) {
		t.Fatalf("public SSE framing/usage/terminal failed: %s", body)
	}
}

func TestInstalledCLIQoderNativeVertical(t *testing.T) {
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		t.Skip("dedicated installed gate requires GOBROOM_TEST_CLI")
	}
	f := newQoderNativeFixture(t)
	for _, command := range [][]string{{"providers", "catalog"}, {"extensions", "catalog"}, {"config-export"}} {
		raw := portabilityCLI(t, cli, f.host.config.IPCPath, nil, command...)
		if bytes.Contains(raw, []byte("native-private-token")) {
			t.Fatal("ordinary controls leaked imported credential")
		}
	}
	// Exercise an installed-client mutation, not only read-only catalog access.
	bundle := portabilityCLI(t, cli, f.host.config.IPCPath, nil, "config-export")
	portabilityCLI(t, cli, f.host.config.IPCPath, nil, "combo-models", "delete", "--name", "qoder-native-role")
	if status, _ := f.call(t); status < 400 || f.calls.Load() != 0 {
		t.Fatal("deleted role still dispatched inference")
	}
	portabilityCLI(t, cli, f.host.config.IPCPath, bundle, "config-apply")
	for pass := range 2 {
		if pass > 0 {
			f.host.restart(t)
		}
		status, body := f.call(t)
		if status != 200 {
			t.Fatalf("installed host consume HTTP%d %s", status, body)
		}
		var health struct {
			Status  string `json:"status"`
			Control uint64 `json:"controlSnapshotVersion"`
			Serving uint64 `json:"servingSnapshotVersion"`
		}
		if err := json.Unmarshal(portabilityCLI(t, cli, f.host.config.IPCPath, nil, "status"), &health); err != nil || health.Status != "ok" || health.Control == 0 || health.Control != health.Serving {
			t.Fatalf("installed host unhealthy: %#v %v", health, err)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatal("ordinary controls dispatched inference")
	}
}

func TestQoderNativeAcceptedEnvelopeFailureNeverReplays(t *testing.T) {
	for _, mode := range []int32{1, 2} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) {
			f := newQoderNativeFixture(t)
			f.mode.Store(mode)
			status, body := f.call(t)
			if status < 400 || f.calls.Load() != 1 || bytes.Contains(body, []byte("private-native-quota")) || bytes.Contains(body, []byte("native-private-token")) {
				t.Fatalf("accepted failure replayed/succeeded/leaked: HTTP%d calls=%d body=%s", status, f.calls.Load(), body)
			}
		})
	}
}

func TestQoderNativeRejectsUnpreservedSemanticsBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"response-format", func(body map[string]any) { body["response_format"] = map[string]any{"type": "json_object"} }},
		{"seed", func(body map[string]any) { body["seed"] = 7 }},
		{"n", func(body map[string]any) { body["n"] = 2 }},
		{"logprobs", func(body map[string]any) { body["logprobs"] = true }},
		{"named-message", func(body map[string]any) {
			body["messages"] = []map[string]any{{"role": "system", "content": "native system"}, {"role": "user", "content": "native hello", "name": "caller-name"}}
		}},
		{"multipart-text", func(body map[string]any) {
			body["messages"] = []map[string]any{{"role": "system", "content": "native system"}, {"role": "user", "content": []map[string]string{{"type": "text", "text": "native hello"}}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newQoderNativeFixture(t)
			body := map[string]any{"model": "qoder-native-role", "messages": []map[string]string{{"role": "system", "content": "native system"}, {"role": "user", "content": "native hello"}}, "max_tokens": 64}
			test.change(body)
			raw := portabilityOK(t, f.host.config.IPCPath, "routes.explain", map[string]any{"model": "qoder-native-role", "request": body})
			var rows []struct {
				Compatibility []struct {
					Compatible bool `json:"compatible"`
				} `json:"compatibility"`
			}
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || len(rows[0].Compatibility) != 1 || rows[0].Compatibility[0].Compatible {
				t.Errorf("unpreserved semantics admitted: %s", raw)
			}
			status, response := sg1Post(t, f.host, "/v1/chat/completions", body, "")
			if status < 400 || f.calls.Load() != 0 {
				t.Fatalf("unpreserved semantics dispatched: HTTP%d calls=%d body=%s", status, f.calls.Load(), response)
			}
		})
	}
}

func TestQoderNativeRejectsAmbiguousSigningBase(t *testing.T) {
	credential, err := (qoderFixtureAuth{}).Resolve(t.Context(), provider.AuthInput{ConnectionID: "fixture", Type: "qoder_oauth", Secret: `{"access_token":"fixture-token","user_id":"fixture-user","machine_id":"fixture-machine"}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://host.invalid?tenant=x", "https://host.invalid/prefix"} {
		if _, err := qoderPreparePayload(t.Context(), provider.NormalizedRequest{}, provider.Route{BaseURL: base, ExternalModel: "qmodel_latest"}, credential, []byte(`{}`)); err == nil {
			t.Errorf("ambiguous signing scope accepted: %s", base)
		}
	}
}

func TestQoderNativeEndpointSigningScope(t *testing.T) {
	endpoint := qoderNativeEndpoint{}
	resolved, err := endpoint.Resolve("https://host.invalid/", qoderFixturePath, provider.EndpointOptions{})
	if err != nil || resolved != "https://host.invalid"+qoderFixturePath {
		t.Fatalf("fixed signing endpoint: %q %v", resolved, err)
	}
	for _, options := range []provider.EndpointOptions{{Path: "/other"}, {Query: map[string]string{"tenant": "other"}}} {
		if _, err := endpoint.Resolve("https://host.invalid", qoderFixturePath, options); err == nil {
			t.Errorf("signing endpoint override accepted: %#v", options)
		}
	}
}
