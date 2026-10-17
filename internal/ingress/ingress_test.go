package ingress

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestJSONOperationIngressBindsInlineArtifactsAndRejectsBodyLeases(t *testing.T) {
	codec := JSONOperationIngress{CodecID: "jobs-status", IngressContractVersion: 1, Operation: "jobs.status", OperationContractVersion: 1}
	artifact := extensions.ArtifactRef{TypeRef: extensions.Ref{Kind: extensions.ArtifactKind, ID: "jobs.handle", ContractVersion: 1}, Role: "job", Owner: extensions.ArtifactOwner{Domain: "provider", ConnectionID: "account-a"}, ReplayScope: extensions.ArtifactReplayConnection, Sensitivity: extensions.ArtifactPrivate, MediaType: "application/json", Value: []byte(`{"id":"job-1"}`), SizeBytes: 14}
	envelope := struct {
		Model     string                   `json:"model"`
		Payload   json.RawMessage          `json:"operationPayload"`
		Artifacts []extensions.ArtifactRef `json:"artifacts"`
	}{"job-role", json.RawMessage(`{}`), []extensions.ArtifactRef{artifact}}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs/status", bytes.NewReader(encoded))
	decoded, err := codec.Decode(request)
	if err != nil || len(decoded.Artifacts) != 1 || string(decoded.Artifacts[0].Value) != string(artifact.Value) || decoded.Artifacts[0].Owner.ConnectionID != "account-a" {
		t.Fatalf("typed inline artifact decode=%#v err=%v", decoded.Artifacts, err)
	}
	envelope.Artifacts[0].Value = nil
	envelope.Artifacts[0].Body = &extensions.BodyRef{ID: "private-daemon-spool"}
	encoded, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode(httptest.NewRequest(http.MethodPost, "/v1/jobs/status", bytes.NewReader(encoded))); err == nil {
		t.Fatal("external JSON envelope smuggled a daemon-owned body lease")
	}
}
