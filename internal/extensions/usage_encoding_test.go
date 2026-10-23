package extensions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsageValueOwnedEncodingBoundsRetainedDataAndPreservesPrecision(t *testing.T) {
	value := UsageValue{TypeRef: Ref{Kind: UsageKind, ID: "fixture.precise", ContractVersion: 1}, Value: json.RawMessage(strings.Repeat(" ", 100) + `9007199254740993.012345`)}
	owned, size, err := value.OwnedEncoding(256)
	if err != nil {
		t.Fatal(err)
	}
	if string(owned.Value) != `9007199254740993.012345` || size > 256 {
		t.Fatalf("unbounded or lossy canonical value: %q size=%d", owned.Value, size)
	}
	value.Value[100] = '0'
	if string(owned.Value) != `9007199254740993.012345` {
		t.Fatal("caller mutated owned encoding")
	}
	value.Value = json.RawMessage(`"<&>"`)
	encoded, _ := json.Marshal(value)
	if _, _, err := value.OwnedEncoding(int64(len(encoded) - 1)); err == nil {
		t.Fatal("escaped value bypassed bound")
	}
	if _, _, err := value.OwnedEncoding(int64(len(encoded))); err != nil {
		t.Fatalf("exact encoded bound rejected: %v", err)
	}
}
