package externalprovider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const sg1AnthropicBudgetLoss = "anthropic.reasoning-budget-raises-max-output"

type sg1AnthropicWire struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Thinking  struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking"`
	Tools []struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
	System json.RawMessage `json:"system"`
}

func sg1AnthropicUpstream(t *testing.T, response string) (*httptest.Server, *atomic.Int32, <-chan sg1AnthropicWire) {
	t.Helper()
	calls := &atomic.Int32{}
	captured := make(chan sg1AnthropicWire, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "sg1-fixture-key" {
			t.Errorf("Anthropic target/auth mismatch: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var wire sg1AnthropicWire
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		select {
		case captured <- wire:
		default:
			t.Error("unexpected extra upstream dispatch")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, calls, captured
}

func sg1AssertAnthropicWire(t *testing.T, ch <-chan sg1AnthropicWire) {
	t.Helper()
	select {
	case wire := <-ch:
		if wire.Model != "claude-sg1" || wire.MaxTokens != 3072 || wire.Thinking.Type != "enabled" || wire.Thinking.BudgetTokens != 2048 || len(wire.Tools) != 1 || wire.Tools[0].Name != "lookup" || len(wire.Tools[0].InputSchema) == 0 {
			t.Fatalf("Anthropic wire lost exact budget/tools: %#v", wire)
		}
		var text string
		if err := json.Unmarshal(wire.System, &text); err != nil {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(wire.System, &blocks); err != nil || len(blocks) != 1 || blocks[0].Type != "text" {
				t.Fatalf("invalid transformed system blocks: %s", wire.System)
			}
			text = blocks[0].Text
		}
		if text != sg1PromptText {
			t.Fatalf("prompt transform was not encoded: %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing actual upstream request")
	}
}

func TestSG1AnthropicToolsBudgetRequiresNamedLossGrants(t *testing.T) {
	upstream, calls, captured := sg1AnthropicUpstream(t, `{"type":"message","id":"msg_sg1_budget","role":"assistant","content":[{"type":"text","text":"budget accepted"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":4}}`)
	h := newSG1Host(t)
	m := sg1ConfigureModel(t, h, sg1ModelSpec{Name: "sg1budget", Protocol: "anthropic", DefinitionID: "anthropic", BaseURL: upstream.URL + "/v1", ExternalModel: "claude-sg1"})
	sg1PublishRole(t, h, "sg1-budget", m)
	sg1PutPrompt(t, h, "sg1-budget")
	input := json.RawMessage(`{"model":"sg1-budget","messages":[{"role":"system","content":"client rule"},{"role":"user","content":"Find item 7."}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}}}],"max_tokens":512,"reasoning":{"max_output_tokens":2048}}`)
	setCeiling := func(allow []string) {
		portabilityOK(t, h.config.IPCPath, "compatibility.loss-ceiling.set", map[string]any{"allowOnly": true, "allow": allow})
	}
	setCeiling([]string{sg1AnthropicBudgetLoss})
	if status, body := sg1Post(t, h, "/v1/chat/completions", input, ""); status < 400 || calls.Load() != 0 {
		t.Fatalf("missing model grant dispatched: HTTP%d calls=%d body=%s", status, calls.Load(), body)
	}
	sg1SetLossPolicy(t, h, m, []string{sg1AnthropicBudgetLoss})
	setCeiling(nil)
	if status, body := sg1Post(t, h, "/v1/chat/completions", input, ""); status < 400 || calls.Load() != 0 {
		t.Fatalf("server ceiling bypassed: HTTP%d calls=%d body=%s", status, calls.Load(), body)
	}
	setCeiling([]string{sg1AnthropicBudgetLoss})
	status, body := sg1Post(t, h, "/v1/chat/completions", input, "")
	if status != 200 || calls.Load() != 1 {
		t.Fatalf("approved named loss failed: HTTP%d calls=%d body=%s", status, calls.Load(), body)
	}
	sg1AssertAnthropicWire(t, captured)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var rows []struct {
			Status        string
			ExternalModel string
			Fidelity      string `json:"compatibilityFidelity"`
			Losses        []struct {
				ID        string      `json:"id"`
				Requested json.Number `json:"requested"`
				Effective json.Number `json:"effective"`
			} `json:"compatibilityLosses"`
		}
		raw := portabilityOK(t, h.config.IPCPath, "usage.list", map[string]any{"limit": 10})
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			for _, loss := range row.Losses {
				requested, err1 := loss.Requested.Int64()
				effective, err2 := loss.Effective.Int64()
				if row.Status == "ok" && row.ExternalModel == "claude-sg1" && row.Fidelity == "lossy" && loss.ID == sg1AnthropicBudgetLoss && err1 == nil && err2 == nil && requested == 512 && effective == 3072 {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("selected usage omitted exact named loss: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSG1AnthropicSignedThinkingRejectsSemanticResponseMutation(t *testing.T) {
	upstream, calls, captured := sg1AnthropicUpstream(t, `{"type":"message","id":"msg_sg1_signed","role":"assistant","content":[{"type":"thinking","thinking":"private reasoning","signature":"issuer-signed-thinking"},{"type":"tool_use","id":"toolu_sg1","name":"lookup","input":{"id":7}}],"stop_reason":"tool_use","usage":{"input_tokens":9,"output_tokens":6}}`)
	h := newSG1Host(t)
	m := sg1ConfigureModel(t, h, sg1ModelSpec{Name: "sg1signed", Protocol: "anthropic", DefinitionID: "anthropic", BaseURL: upstream.URL + "/v1", ExternalModel: "claude-sg1"})
	sg1PublishRole(t, h, "sg1-signed", m)
	sg1PutPrompt(t, h, "sg1-signed")
	sg1PutResponse(t, h, "sg1-signed", true)
	input := json.RawMessage(`{"model":"sg1-signed","system":"client rule","max_tokens":3072,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"Find item 7."}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"id":{"type":"integer"}}}}]}`)
	if status, body := sg1Post(t, h, "/v1/messages", input, ""); status < 400 || calls.Load() != 0 {
		t.Fatalf("semantic mutation admitted signed thinking: HTTP%d calls=%d body=%s", status, calls.Load(), body)
	}
	sg1PutResponse(t, h, "sg1-signed", false)
	status, body := sg1Post(t, h, "/v1/messages", input, "")
	if status != 200 || calls.Load() != 1 {
		t.Fatalf("signed native rendering failed: HTTP%d calls=%d body=%s", status, calls.Load(), body)
	}
	sg1AssertAnthropicWire(t, captured)
	var rendered struct {
		Content []struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			Input     struct {
				ID int `json:"id"`
			} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &rendered); err != nil {
		t.Fatal(err)
	}
	if len(rendered.Content) != 2 || rendered.Content[0].Type != "thinking" || rendered.Content[0].Thinking != "private reasoning" || rendered.Content[0].Signature != "issuer-signed-thinking" || rendered.Content[1].Type != "tool_use" || rendered.Content[1].ID != "toolu_sg1" || rendered.Content[1].Name != "lookup" || rendered.Content[1].Input.ID != 7 {
		t.Fatalf("signed reasoning/tool identity changed: %s", body)
	}
}
