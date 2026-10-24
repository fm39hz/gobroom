package externalprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fm39hz/gobroom/pkg/provider"
)

// newFailureClientModule adds a client dialect alongside JobModule. The job
// operation, request codec, result artifacts, and normal renderer remain shared.
type newFailureClientModule struct{}

func (newFailureClientModule) ModuleDescriptor() provider.ModuleRef {
	return provider.ModuleRef{ID: "example.jobs.failure-client", Version: "1.0.0", HostContractVersion: provider.RuntimeModuleHostContractVersion}
}

func (newFailureClientModule) Register(runtime *provider.RuntimeRegistry) error {
	if err := runtime.RegisterSchemaDocument(provider.Ref{Kind: provider.SchemaKind, ID: "example.jobs.failure-client.error", ContractVersion: 1}, json.RawMessage(failureClientErrorSchema)); err != nil {
		return err
	}
	if err := runtime.RegisterResponseDecoderVersion(failureClientDecoder{jobResponseDecoder{action: "create"}}, 2); err != nil {
		return err
	}
	operation := jobOperation("create")
	return runtime.RegisterClientIngress(jobIngress{provider.JSONOperationIngress{
		CodecID: "example.jobs.failure-client.json", IngressContractVersion: 1,
		Operation: operation.ID, OperationContractVersion: operation.ContractVersion,
		Endpoints:       []provider.ClientIngressRoute{{Method: http.MethodPost, Path: "/v1/failure-client/jobs/create"}},
		FailureEncoding: failureClientEncoder{},
	}})
}

// Embedding preserves the real decoder's event declarations and classifier.
// Only an explicit upstream signal selects the example's private decode fault.
type failureClientDecoder struct{ jobResponseDecoder }

func (failureClientDecoder) ID() string { return "example.jobs.failure-client.create" }

func (d failureClientDecoder) Decode(ctx context.Context, response provider.UpstreamResponse, emit func(provider.ResponseEvent) error, hooks provider.StreamHooks) error {
	if response.Headers.Get("X-Example-Private-Decode-Failure") == "true" {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return errors.New("private-job-decoder-cause-4b7e")
	}
	return d.jobResponseDecoder.Decode(ctx, response, emit, hooks)
}

type failureClientEncoder struct{}

func (failureClientEncoder) RenderFailure(_ context.Context, input provider.ClientFailureContext) (provider.ClientFailureResponse, error) {
	if !input.Valid() {
		return provider.ClientFailureResponse{}, errors.New("invalid job client failure context")
	}
	input = input.Clone()
	var envelope struct {
		Declined struct {
			Tag         provider.ClientFailureCode `json:"tag"`
			Category    string                     `json:"category"`
			Description string                     `json:"description"`
		} `json:"declined"`
		Receipt struct {
			Caller string                  `json:"caller,omitempty"`
			Task   provider.Ref            `json:"task"`
			HTTP   int                     `json:"http"`
			Effect provider.UpstreamEffect `json:"effect"`
			Retry  provider.RetryAction    `json:"retry"`
			Model  string                  `json:"model,omitempty"`
		} `json:"receipt"`
		Hints *provider.ClientFailureDiagnostics `json:"hints,omitempty"`
	}
	envelope.Declined.Tag = input.Failure.Code
	envelope.Declined.Category = input.Failure.Type
	envelope.Declined.Description = input.Failure.Message
	envelope.Receipt.Caller = input.RequestID
	envelope.Receipt.Task = input.OperationRef
	envelope.Receipt.HTTP = input.Status
	envelope.Receipt.Effect = input.Effect
	envelope.Receipt.Retry = input.Retry
	envelope.Receipt.Model = input.Model
	if len(input.Diagnostics.Paths) != 0 || len(input.Diagnostics.Reasons) != 0 {
		envelope.Hints = &input.Diagnostics
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return provider.ClientFailureResponse{}, errors.New("encode job client failure")
	}
	response := provider.ClientFailureResponse{ContentType: "application/vnd.example.job-declined+json", Body: string(encoded)}
	if !response.Valid() {
		return provider.ClientFailureResponse{}, errors.New("invalid job client failure response")
	}
	return response, nil
}

// This wire schema is a module contract, separate from the job result schemas.
// Optional correlation values may be omitted by the host's bounding policy.
const failureClientErrorSchema = `{
  "type":"object","additionalProperties":false,
  "required":["declined","receipt"],
  "properties":{
    "declined":{"type":"object","additionalProperties":false,
      "required":["tag","category","description"],"properties":{
        "tag":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[A-Za-z0-9_.:-]+$"},
        "category":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[A-Za-z0-9_.:-]+$"},
        "description":{"type":"string","minLength":1,"maxLength":4096}}},
    "receipt":{"type":"object","additionalProperties":false,
      "required":["task","http","effect","retry"],"properties":{
        "caller":{"type":"string","minLength":1,"maxLength":128},
        "task":{"type":"object","additionalProperties":false,
          "required":["kind","id","contractVersion"],"properties":{
            "kind":{"const":"operation"},"id":{"type":"string","minLength":1},
            "contractVersion":{"type":"integer","minimum":1}}},
        "http":{"type":"integer","minimum":400,"maximum":599},
        "effect":{"enum":["not_dispatched","confirmed_rejection","accepted","unknown"]},
        "retry":{"enum":["retry_never","retry_now","retry_after","retry_unknown"]},
        "model":{"type":"string","minLength":1,"maxLength":128}}},
    "hints":{"type":"object","additionalProperties":false,"properties":{
      "paths":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":256,"pattern":"^/"}},
      "reasons":{"type":"array","maxItems":32,"items":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[A-Za-z0-9_.:-]+$"}}}}
  }
}`
