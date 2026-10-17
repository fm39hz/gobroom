package kernel

import (
	"testing"
	"time"
)

func TestIdempotencyPolicyRequiresExplicitBoundedRetention(t *testing.T) {
	for _, policy := range []IdempotencyPolicy{
		{}, {Header: "X-Key"}, {Header: "X-Key", MinRetentionSeconds: -1},
		{Header: "X-Key", MinRetentionSeconds: 1<<63 - 1},
		{Header: "bad\r\nInjected: yes", MinRetentionSeconds: 60},
		{Header: "Authorization", MinRetentionSeconds: 60},
		{Header: "Connection", MinRetentionSeconds: 60},
	} {
		if err := policy.Validate(); err == nil {
			t.Fatalf("invalid idempotency policy accepted: %#v", policy)
		}
	}
	policy := IdempotencyPolicy{Header: "X-Key", MinRetentionSeconds: 60}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if !policy.permitsReplay(start, start.Add(59*time.Second)) || policy.permitsReplay(start, start.Add(time.Minute)) || policy.permitsReplay(start, start.Add(-time.Second)) || policy.permitsReplay(time.Time{}, start) {
		t.Fatal("replay does not respect its original, half-open retention window")
	}
}
