package operations

import (
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/normalize"
)

func TestChatDefinitionCompilesTypedInputAndPinsReplayContract(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterChatGenerate(catalog, registry); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.Prepare(normalize.Request{
		Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1,
		Messages:     []normalize.Message{{Role: "user", Content: "transcribe then summarize"}},
		Requirements: []normalize.FeatureRequirement{{ID: "vendor.example.json-output.v1", Constraints: json.RawMessage(`{"format":"strict"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Ref != ChatGenerateRef() || prepared.ReplaySafety != ReplayOnRejection || len(prepared.Requirements) != 1 {
		t.Fatalf("prepared operation=%#v", prepared)
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "junior", Operation: normalize.OperationChatGenerate}); err == nil {
		t.Fatal("operation without its registered contract version was accepted")
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 2}); err == nil {
		t.Fatal("unsupported operation contract version was accepted")
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "junior", Operation: normalize.OperationChatGenerate, OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"audio":"not a chat payload"}`)}); err == nil {
		t.Fatal("chat operation accepted an opaque non-chat payload")
	}
}

func TestRegisteredNonChatOperationValidatesItsOwnPayload(t *testing.T) {
	catalog := extensions.NewCatalog()
	registry, err := NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	ref := extensions.Ref{Kind: "operation", ID: "audio.transcribe.v1", ContractVersion: 1}
	if err := registry.RegisterRawPayload(ref, "Transcribe audio", "Return a transcript from audio bytes.", json.RawMessage(`{"type":"object","properties":{"contentType":{"const":"audio/wav"},"data":{"type":"string","minLength":1}},"required":["contentType","data"],"additionalProperties":false}`), ReplayNever); err != nil {
		t.Fatal(err)
	}
	extensionSnapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Seal(extensionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.Prepare(normalize.Request{Model: "asr", Operation: normalize.Operation("audio.transcribe.v1"), OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"contentType":"audio/wav","data":"AAAA"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Ref != ref || prepared.ReplaySafety != ReplayNever {
		t.Fatalf("prepared audio operation=%#v", prepared)
	}
	if _, err := snapshot.Prepare(normalize.Request{Model: "asr", Operation: normalize.Operation("audio.transcribe.v1"), OperationContractVersion: 1, OperationPayload: json.RawMessage(`{"audio":"AAAA"}`)}); err == nil {
		t.Fatal("audio payload that violates the operation schema was accepted")
	}
}
