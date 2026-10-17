package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHealthEntryShowsIndependentOperationEvidence(t *testing.T) {
	raw := json.RawMessage(`{"route":{"LastScope":"operation","LastCause":"success","operations":[{"operationRef":{"kind":"operation","id":"job.cancel","contractVersion":1},"LastCause":"network","Failures":1,"CooldownUntil":"2099-01-01T00:00:00Z"},{"operationRef":{"kind":"operation","id":"job.status","contractVersion":1},"LastCause":"success","Successes":1}]}}`)
	entries, err := makeEntries("health.list", raw, nil)
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].detail, "operation:job.cancel@1 · cooldown") || !strings.Contains(entries[0].detail, "operation:job.status@1 · healthy") {
		t.Fatalf("operation evidence hidden in health view: %#v err=%v", entries, err)
	}
}

func TestQuotaEntryIdentityIncludesExactOperation(t *testing.T) {
	raw := json.RawMessage(`[{"ProviderNodeID":"p","ConnectionID":"c","ModelRef":"m","WindowName":"daily","operationRef":{"kind":"operation","id":"job.create","contractVersion":1}},{"ProviderNodeID":"p","ConnectionID":"c","ModelRef":"m","WindowName":"daily","operationRef":{"kind":"operation","id":"job.create","contractVersion":2}}]`)
	entries, err := makeEntries("quota.list", raw, nil)
	if err != nil || len(entries) != 2 || entries[0].key == entries[1].key {
		t.Fatalf("operation quota rows share a selection identity: %#v err=%v", entries, err)
	}
	if !strings.Contains(entries[0].detail, "operation:job.create@") || !strings.Contains(entries[1].title, "operation:job.create@") {
		t.Fatal("quota operation/version is not visible")
	}
}
