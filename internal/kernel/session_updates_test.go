package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSessionUpdatesPublishOnlyAcceptedPrivateState(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted"}[accept], func(t *testing.T) {
			updates := &sessionUpdates{ctx: context.Background()}
			updates.capture(SessionState{ResponseID: "response-1", ProviderData: json.RawMessage(`{"cursor":1}`)})
			var published []SessionState
			if err := updates.finish(func(state SessionState) { published = append(published, state) }, accept); err != nil {
				t.Fatal(err)
			}
			if got := len(published); accept && got != 1 || !accept && got != 0 {
				t.Fatalf("published %d updates for accept=%v", got, accept)
			}
			if accept && published[0].ResponseID != "response-1" {
				t.Fatalf("published state=%#v", published[0])
			}
		})
	}
}

func TestSessionUpdatesEmptyStatePublishesResetBeforeLaterState(t *testing.T) {
	updates := &sessionUpdates{ctx: context.Background()}
	updates.capture(SessionState{ResponseID: "stale"})
	updates.capture(SessionState{})
	updates.capture(SessionState{ResponseID: "fresh"})
	var published []SessionState
	if err := updates.finish(func(state SessionState) { published = append(published, state) }, true); err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 || !emptySessionState(published[0]) || published[1].ResponseID != "fresh" {
		t.Fatalf("reset and subsequent state were not published in order: %#v", published)
	}
}

func TestSessionUpdatesCloneStateAndIgnoreLateCallback(t *testing.T) {
	updates := &sessionUpdates{ctx: context.Background()}
	data := json.RawMessage(`{"cursor":1}`)
	updates.capture(SessionState{ResponseID: "before", ProviderData: data})
	data[10] = 'X'
	var published []SessionState
	if err := updates.finish(func(state SessionState) { published = append(published, state) }, true); err != nil {
		t.Fatal(err)
	}
	updates.capture(SessionState{ResponseID: "late"})
	if len(published) != 1 || published[0].ResponseID != "before" || string(published[0].ProviderData) != `{"cursor":1}` {
		t.Fatalf("published state=%#v", published)
	}
	published[0].ProviderData[2] = 'X'
	if string(data) == string(published[0].ProviderData) {
		t.Fatal("published provider data aliases caller-owned input")
	}
}

func TestSessionUpdatesRejectInvalidStateWithoutPublishing(t *testing.T) {
	updates := &sessionUpdates{ctx: context.Background()}
	updates.capture(SessionState{ProviderData: json.RawMessage(`not-json`)})
	var published bool
	err := updates.finish(func(SessionState) { published = true }, true)
	if err == nil || published {
		t.Fatalf("invalid state result err=%v published=%v", err, published)
	}
}

func TestSessionUpdatesContextCancellationRejectsCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	updates := &sessionUpdates{ctx: ctx}
	cancel()
	updates.capture(SessionState{ResponseID: "cancelled"})
	if err := updates.finish(func(SessionState) { t.Fatal("cancelled state was published") }, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("finish error=%v, want context canceled", err)
	}
}
