package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestHealthGateCooldownAndRecovery(t *testing.T) {
	gate := NewHealthGate()
	route := kernel.Route{ID: "route-a", Enabled: true}
	if !gate.Usable(route, time.Now()) {
		t.Fatal("new route should be usable")
	}
	gate.MarkFailure(route, kernel.ErrorCooldown, errors.New("429"))
	if gate.Usable(route, time.Now()) {
		t.Fatal("failed route should be cooled down")
	}
	gate.MarkSuccess(route)
	if !gate.Usable(route, time.Now()) {
		t.Fatal("successful route should recover")
	}
}
