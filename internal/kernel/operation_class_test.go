package kernel

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestWorkloadClassIncludesExactOperationWithoutAStringDSL(t *testing.T) {
	request := NormalizedRequest{Operation: normalize.Operation("job.create"), OperationContractVersion: 1, SourceFormat: normalize.FormatOpenAIChat}
	first := requestClass(request)
	var decoded struct {
		Operation extensions.Ref `json:"operation"`
	}
	if err := json.Unmarshal([]byte(first), &decoded); err != nil || decoded.Operation != (extensions.Ref{Kind: "operation", ID: "job.create", ContractVersion: 1}) {
		t.Fatalf("workload identity is not a typed JSON binding: %s %v", first, err)
	}
	request.OperationContractVersion = 2
	if requestClass(request) == first {
		t.Fatal("operation versions share a performance class")
	}
	request.Operation = normalize.Operation("job.status")
	request.OperationContractVersion = 1
	if requestClass(request) == first {
		t.Fatal("different operations share a performance class")
	}
}
