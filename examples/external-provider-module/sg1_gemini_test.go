package externalprovider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/pkg/provider"
)

type sg1Reasoning struct {
	MaxOutputTokens int `json:"max_output_tokens"`
}
type sg1ChatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}
type sg1ChatToolCall struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	Function         sg1ChatFunction `json:"function"`
}
type sg1ChatMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []sg1ChatToolCall `json:"tool_calls,omitempty"`
}
type sg1ChatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"function"`
}
type sg1ChatRequest struct {
	Model              string           `json:"model"`
	Messages           []sg1ChatMessage `json:"messages"`
	Tools              []sg1ChatTool    `json:"tools"`
	Reasoning          sg1Reasoning     `json:"reasoning"`
	MaxTokens          int              `json:"max_tokens"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
}
type sg1ChatReply struct {
	Choices []struct {
		Message struct {
			Content   string            `json:"content"`
			ToolCalls []sg1ChatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}
type sg1GeminiFixture struct {
	host                     *portabilityHost
	calls, incompatibleCalls atomic.Int32
}

func newSG1GeminiFixture(t *testing.T) *sg1GeminiFixture {
	t.Helper()
	f := &sg1GeminiFixture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-3-flash:generateContent" || r.Header.Get("x-goog-api-key") != "sg1-fixture-key" {
			t.Errorf("Gemini target/auth mismatch: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var wire struct {
			Messages json.RawMessage `json:"messages"`
			System   struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text             string `json:"text"`
					ThoughtSignature string `json:"thoughtSignature"`
					Call             *struct {
						ID, Name string
						Args     json.RawMessage
					} `json:"functionCall"`
					Result *struct {
						Name     string
						Response json.RawMessage
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"contents"`
			Generation struct {
				Thinking struct {
					Budget int `json:"thinkingBudget"`
				} `json:"thinkingConfig"`
			} `json:"generationConfig"`
			Tools []struct {
				Functions []struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(wire.Messages) != 0 || wire.Generation.Thinking.Budget != 2048 || len(wire.System.Parts) != 1 || wire.System.Parts[0].Text != sg1PromptText || len(wire.Tools) != 1 || len(wire.Tools[0].Functions) != 1 || wire.Tools[0].Functions[0].Name != "lookup" {
			t.Errorf("combined budget/tools/transform missing: %#v", wire)
			w.WriteHeader(400)
			return
		}
		if len(wire.Contents) != 1 && len(wire.Contents) != 3 {
			t.Errorf("wrong Gemini history count: %d", len(wire.Contents))
			w.WriteHeader(400)
			return
		}
		if len(wire.Contents) == 3 {
			if len(wire.Contents[1].Parts) != 1 || len(wire.Contents[2].Parts) != 1 {
				t.Error("tool turn parts lost")
				w.WriteHeader(400)
				return
			}
			call, result := wire.Contents[1].Parts[0], wire.Contents[2].Parts[0]
			if call.Call == nil || call.Call.ID != "sg1-call-7" || call.Call.Name != "lookup" || call.ThoughtSignature != "private-sg1-signature-7" || result.Result == nil || result.Result.Name != "lookup" {
				t.Errorf("scoped tool continuation not preserved: %#v %#v", call, result)
				w.WriteHeader(400)
				return
			}
			var args struct {
				ID int `json:"id"`
			}
			var output struct {
				Found bool `json:"found"`
			}
			if err := json.Unmarshal(call.Call.Args, &args); err != nil || args.ID != 7 {
				t.Error("tool arguments changed")
			}
			if err := json.Unmarshal(result.Result.Response, &output); err != nil || !output.Found {
				t.Error("tool result changed")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"fixture"},{"functionCall":{"id":"sg1-call-7","name":"lookup","args":{"id":7}},"thoughtSignature":"private-sg1-signature-7"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":6,"thoughtsTokenCount":0}}`)
	}))
	t.Cleanup(upstream.Close)
	incompatible := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { f.incompatibleCalls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(incompatible.Close)
	f.host = newSG1Host(t)
	first := sg1ConfigureModel(t, f.host, sg1ModelSpec{Name: "sg1chat", Protocol: "openai_chat", DefinitionID: "openai-compatible-chat", BaseURL: incompatible.URL + "/v1", ExternalModel: "chat-only"})
	second := sg1ConfigureModel(t, f.host, sg1ModelSpec{Name: "sg1gemini", Protocol: "gemini", DefinitionID: "gemini", BaseURL: upstream.URL + "/v1beta", ExternalModel: "gemini-3-flash"})
	sg1PublishRole(t, f.host, "sg1-role", first, second)
	sg1PutPrompt(t, f.host, "sg1-role")
	sg1PutResponse(t, f.host, "sg1-role", true)
	return f
}

func (f *sg1GeminiFixture) send(t *testing.T, input sg1ChatRequest, session string) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodPost, "http://"+f.host.service.Addresses().HTTP+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer client-key-4b7e")
	r.Header.Set("X-Session-ID", session)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

// seed consumes a real provider response and returns the valid client follow-up.
// No kernel/cache internals or manufactured issuer/proof are used.
func (f *sg1GeminiFixture) seed(t *testing.T) sg1ChatRequest {
	t.Helper()
	tool := sg1ChatTool{Type: "function"}
	tool.Function.Name = "lookup"
	tool.Function.Parameters = json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`)
	request := sg1ChatRequest{Model: "sg1-role", Messages: []sg1ChatMessage{{Role: "system", Content: "client rule"}, {Role: "user", Content: "Find item 7."}}, Tools: []sg1ChatTool{tool}, Reasoning: sg1Reasoning{MaxOutputTokens: 2048}, MaxTokens: 4096}
	status, raw := f.send(t, request, "sg1-session")
	var reply sg1ChatReply
	if err := json.Unmarshal(raw, &reply); err != nil || status != 200 || len(reply.Choices) != 1 || len(reply.Choices[0].Message.ToolCalls) != 1 || reply.Choices[0].Message.Content != "fixture-managed" || reply.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("seed failed: HTTP%d body=%s err=%v", status, raw, err)
	}
	call := reply.Choices[0].Message.ToolCalls[0]
	if call.ID != "sg1-call-7" || call.Function.Name != "lookup" || call.ThoughtSignature != "private-sg1-signature-7" {
		t.Fatalf("client signature/tool identity lost: %#v", call)
	}
	request.Messages = append(request.Messages, sg1ChatMessage{Role: "assistant", ToolCalls: []sg1ChatToolCall{call}}, sg1ChatMessage{Role: "tool", ToolCallID: call.ID, Content: `{"found":true}`})
	return request
}

func TestSG1GeminiToolsBudgetScopedHistoryAndTransforms(t *testing.T) {
	f := newSG1GeminiFixture(t)
	request := f.seed(t)
	status, raw := f.send(t, request, "sg1-session")
	var reply sg1ChatReply
	if err := json.Unmarshal(raw, &reply); err != nil || status != 200 || len(reply.Choices) != 1 || reply.Choices[0].Message.Content != "fixture-managed" {
		t.Fatalf("same-session continuation failed: HTTP%d body=%s err=%v", status, raw, err)
	}
	if f.calls.Load() != 2 || f.incompatibleCalls.Load() != 0 {
		t.Fatalf("wrong candidate dispatch: Gemini=%d incompatible=%d", f.calls.Load(), f.incompatibleCalls.Load())
	}
}

func sg1AssertSignedPreflight(t *testing.T, f *sg1GeminiFixture, raw []byte, wantGemini bool) {
	t.Helper()
	var rows []struct {
		Route         struct{ DefinitionID string } `json:"route"`
		Compatibility []struct {
			Compatible bool `json:"compatible"`
		} `json:"compatibility"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("preflight omitted candidates: %s", raw)
	}
	gemini, unsupported, seenGemini := false, false, false
	for _, row := range rows {
		if len(row.Compatibility) != 1 {
			t.Fatalf("preflight omitted candidate path: %s", raw)
		}
		if row.Route.DefinitionID == "gemini" {
			seenGemini = true
			gemini = row.Compatibility[0].Compatible
		}
		if row.Route.DefinitionID == "openai-compatible-chat" {
			unsupported = !row.Compatibility[0].Compatible
		}
	}
	if !seenGemini || gemini != wantGemini || !unsupported {
		t.Fatalf("preflight disagrees with signed-history execution: %s", raw)
	}
	if f.calls.Load() != 1 || f.incompatibleCalls.Load() != 0 {
		t.Fatal("preflight dispatched inference")
	}
	if bytes.Contains(raw, []byte("private-sg1-signature-7")) || bytes.Contains(raw, []byte("sg1-fixture-key")) {
		t.Fatal("preflight disclosed signature/credential")
	}
}

func TestSG1SignedPreflightMatchesScopedExecution(t *testing.T) {
	f := newSG1GeminiFixture(t)
	request := f.seed(t)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	raw := portabilityOK(t, f.host.config.IPCPath, "routes.explain", map[string]any{"model": request.Model, "sessionID": "sg1-session", "request": portabilityParams(t, encoded)})
	sg1AssertSignedPreflight(t, f, raw, true)
}

func TestInstalledCLISG1Composition(t *testing.T) {
	cli := os.Getenv("GOBROOM_TEST_CLI")
	if cli == "" {
		t.Skip("dedicated installed SG1 gate requires GOBROOM_TEST_CLI")
	}
	f := newSG1GeminiFixture(t)
	request := f.seed(t)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	preflight := portabilityCLI(t, cli, f.host.config.IPCPath, encoded, "route-explain", "--model", request.Model, "--session", "sg1-session", "--request-file", "-")
	sg1AssertSignedPreflight(t, f, preflight, true)
	if status, body := f.send(t, request, "sg1-session"); status != 200 {
		t.Fatalf("continuation HTTP%d: %s", status, body)
	}
	var bindings []provider.TransformBinding
	if err := json.Unmarshal(portabilityCLI(t, cli, f.host.config.IPCPath, nil, "transforms", "list"), &bindings); err != nil || len(bindings) != 2 {
		t.Fatalf("installed transform control lost bindings: %#v err=%v", bindings, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw := portabilityCLI(t, cli, f.host.config.IPCPath, nil, "usage", "--limit", "10")
		if strings.Contains(string(raw), "private-sg1-signature-7") || strings.Contains(string(raw), "sg1-fixture-key") {
			t.Fatal("ordinary usage disclosed private signature/credential")
		}
		var rows []struct {
			Status, ExternalModel string
			Plan                  *struct {
				Supported bool `json:"supported"`
				Mappings  []struct {
					Facet       string `json:"facet"`
					Disposition string `json:"disposition"`
				} `json:"mappings"`
				Request  []provider.RequestTransformPlanStep  `json:"requestTransformSteps"`
				Response []provider.ResponseTransformPlanStep `json:"responseTransformSteps"`
			} `json:"compatibilityPlan"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 2 {
			for _, row := range rows {
				if row.Status != "ok" || row.ExternalModel != "gemini-3-flash" || row.Plan == nil || !row.Plan.Supported || len(row.Plan.Request) != 1 || len(row.Plan.Response) != 1 || row.Plan.Request[0].TransformRef != (sg1PromptTransform{}).Definition().Ref || row.Plan.Response[0].TransformRef != (responseLifecycleTransform{}).Definition().Ref {
					t.Fatalf("installed usage omitted selected plan: %s", raw)
				}
				budget := false
				for _, mapping := range row.Plan.Mappings {
					if mapping.Facet == provider.FacetReasoningBudget && mapping.Disposition == "translated" {
						budget = true
					}
				}
				if !budget {
					t.Fatalf("installed usage omitted budget mapping: %s", raw)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("installed usage did not persist both dispatches")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status struct {
		Status  string `json:"status"`
		Control uint64 `json:"controlSnapshotVersion"`
		Serving uint64 `json:"servingSnapshotVersion"`
	}
	if err := json.Unmarshal(portabilityCLI(t, cli, f.host.config.IPCPath, nil, "status"), &status); err != nil || status.Status != "ok" || status.Control == 0 || status.Control != status.Serving {
		t.Fatalf("installed status=%#v err=%v", status, err)
	}
	if f.calls.Load() != 2 || f.incompatibleCalls.Load() != 0 {
		t.Fatal("management commands dispatched extra inference")
	}
}
