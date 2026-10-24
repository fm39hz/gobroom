package externalprovider

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSG1GeminiRejectsInvalidSignedContinuationsBeforeDispatch(t *testing.T) {
	cases := []struct {
		name    string
		session string
		mutate  func(*testing.T, *sg1ChatRequest)
	}{
		{
			name:    "wrong session",
			session: "sg1-other-session",
		},
		{
			name:    "changed conversation branch",
			session: "sg1-session",
			mutate: func(t *testing.T, request *sg1ChatRequest) {
				t.Helper()
				if len(request.Messages) < 2 || request.Messages[1].Role != "user" || request.Messages[1].Content != "Find item 7." {
					t.Fatalf("seed user turn did not match expected branch: %#v", request.Messages)
				}
				request.Messages[1].Content = "Find a different item."
			},
		},
		{
			name:    "changed signed tool target",
			session: "sg1-session",
			mutate: func(t *testing.T, request *sg1ChatRequest) {
				t.Helper()
				found := 0
				for i := range request.Messages {
					if request.Messages[i].Role != "assistant" {
						continue
					}
					for j := range request.Messages[i].ToolCalls {
						call := &request.Messages[i].ToolCalls[j]
						if call.ID != "sg1-call-7" {
							continue
						}
						var arguments struct {
							ID int `json:"id"`
						}
						if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
							t.Fatalf("decode seed tool arguments %q: %v", call.Function.Arguments, err)
						}
						if call.Function.Name != "lookup" || call.ThoughtSignature != "private-sg1-signature-7" || arguments.ID != 7 {
							t.Fatalf("seed tool call did not match expected signed target: %#v", *call)
						}
						call.Function.Arguments = `{"id":99}`
						found++
					}
				}
				if found != 1 {
					t.Fatalf("found %d expected signed tool calls; history=%#v", found, request.Messages)
				}
			},
		},
		{
			name:    "unsupported previous response id",
			session: "sg1-session",
			mutate: func(_ *testing.T, request *sg1ChatRequest) {
				request.PreviousResponseID = "resp_foreign"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSG1GeminiFixture(t)
			valid := fixture.seed(t)
			original, err := json.Marshal(valid)
			if err != nil {
				t.Fatal(err)
			}
			var request sg1ChatRequest
			if err := json.Unmarshal(original, &request); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(t, &request)
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			preflight := portabilityOK(t, fixture.host.config.IPCPath, "routes.explain", map[string]any{"model": request.Model, "sessionID": tc.session, "request": portabilityParams(t, encoded)})
			sg1AssertSignedPreflight(t, fixture, preflight, false)
			status, body := fixture.send(t, request, tc.session)
			if status < 400 || status >= 600 {
				t.Fatalf("invalid continuation returned HTTP %d: %s", status, body)
			}
			if got := fixture.calls.Load(); got != 1 {
				t.Errorf("invalid continuation reached Gemini: calls=%d, want only the seed call", got)
			}
			if got := fixture.incompatibleCalls.Load(); got != 0 {
				t.Errorf("invalid continuation reached incompatible fallback: calls=%d, want 0", got)
			}
			if bytes.Contains(body, []byte("private-sg1-signature-7")) || bytes.Contains(body, []byte("fixture")) {
				t.Errorf("error response leaked signature or provider text: %s", strings.TrimSpace(string(body)))
			}
			if status, body := fixture.send(t, valid, "sg1-session"); status != 200 {
				t.Fatalf("rejection poisoned valid scoped history: HTTP%d body=%s", status, body)
			}
			if fixture.calls.Load() != 2 || fixture.incompatibleCalls.Load() != 0 {
				t.Fatal("valid recovery was not a single compatible dispatch")
			}
		})
	}
}
