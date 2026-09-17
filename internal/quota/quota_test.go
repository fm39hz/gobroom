package quota

import (
	"testing"
	"time"
)

func TestExpiredQuotaResetIsUsable(t *testing.T) {
	remaining := float64(0)
	reset := time.Now().Add(-time.Minute)
	s := Snapshot{Remaining: &remaining, ResetAt: &reset}
	if !s.Usable(time.Now()) {
		t.Fatal("quota should be usable after reset")
	}
}

func TestActiveExhaustedQuotaIsBlocked(t *testing.T) {
	remaining := float64(0)
	reset := time.Now().Add(time.Minute)
	s := Snapshot{Remaining: &remaining, ResetAt: &reset}
	if s.Usable(time.Now()) {
		t.Fatal("active exhausted quota should be blocked")
	}
}
