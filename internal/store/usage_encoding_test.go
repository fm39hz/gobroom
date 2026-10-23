package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestUsageHistoryBoundsUseActualJSONEncoding(t *testing.T) {
	for _, offset := range []int64{-1, 0} {
		s, err := Open(t.TempDir() + "/encoding.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		catalog := storeUsageCatalog(t, `{"type":"string"}`, "Escaped value")
		if err := s.SetUsageCatalog(catalog); err != nil {
			t.Fatal(err)
		}
		value := extensions.UsageValue{TypeRef: usageHistoryRef, Value: json.RawMessage(`"<&>"`)}
		encoded, err := json.Marshal([]extensions.UsageValue{value})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetUsageLimits(kernel.UsageLimits{MaxValues: 32, MaxExtensionBytes: int64(len(encoded)) + offset}); err != nil {
			t.Fatal(err)
		}
		err = s.SaveUsageEvent(context.Background(), kernel.UsageEvent{At: time.Now(), Extensions: []extensions.UsageValue{value}})
		if offset < 0 && err == nil {
			t.Fatal("escaped JSON envelope exceeded configured bound but was persisted")
		}
		if offset == 0 && err != nil {
			t.Fatalf("exact encoded bound rejected: %v", err)
		}
	}
}
