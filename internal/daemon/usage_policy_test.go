package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	usageworker "github.com/fm39hz/gobroom/internal/usage"
)

func TestUsagePolicyDefaultCaptureDoesNotGrantDisclosure(t *testing.T) {
	policy := DefaultUsagePolicy()
	if policy.Validate() != nil || policy.CaptureRaw || policy.Retention != 30*24*time.Hour {
		t.Fatalf("invalid safe defaults: %#v", policy)
	}
	policy.CaptureRaw = true
	if err := policy.Authorize(usageworker.DetailOptions{IncludeExtensions: true, IncludePrivate: true, IncludeRaw: true}); err == nil {
		t.Fatal("capture enabled disclosure")
	}
}

func TestUsageDetailDeniedBeforeRepositoryAccess(t *testing.T) {
	d := &Daemon{usagePolicy: DefaultUsagePolicy()}
	response := d.handleIPC(context.Background(), IPCRequest{Method: "usage.detail", Params: map[string]any{"id": 1, "includeExtensions": true, "includePrivate": true, "includeRaw": true}})
	if response.OK || !strings.Contains(response.Error, "disabled by host policy") {
		t.Fatalf("detail request did not enforce host grants before accessing absent repository: %#v", response)
	}
}

func TestUsagePolicyIndependentDetailGrantsAndPositiveBounds(t *testing.T) {
	for _, mutate := range []func(*UsagePolicy){func(p *UsagePolicy) { p.MaxValues = 0 }, func(p *UsagePolicy) { p.MaxExtensionBytes = -1 }, func(p *UsagePolicy) { p.MaxRawBytes = 0 }, func(p *UsagePolicy) { p.Retention = 0 }, func(p *UsagePolicy) { p.AllowPrivateDetails = true }} {
		p := DefaultUsagePolicy()
		mutate(&p)
		if p.Validate() == nil {
			t.Fatalf("invalid policy accepted: %#v", p)
		}
	}
	p := DefaultUsagePolicy()
	p.AllowManagementDetails = true
	if p.Authorize(usageworker.DetailOptions{IncludeExtensions: true}) != nil {
		t.Fatal("management grant denied public extensions")
	}
	if p.Authorize(usageworker.DetailOptions{IncludeExtensions: true, IncludePrivate: true}) == nil || p.Authorize(usageworker.DetailOptions{IncludeRaw: true}) == nil {
		t.Fatal("management grant disclosed private/raw")
	}
	p.AllowPrivateDetails = true
	if p.Authorize(usageworker.DetailOptions{IncludeExtensions: true, IncludePrivate: true}) != nil || p.Authorize(usageworker.DetailOptions{IncludeRaw: true}) == nil {
		t.Fatal("private and raw grants coupled")
	}
	p.AllowRawDetails = true
	if p.Authorize(usageworker.DetailOptions{IncludeRaw: true}) != nil {
		t.Fatal("explicit raw grant denied")
	}
	if p.Authorize(usageworker.DetailOptions{IncludePrivate: true}) == nil {
		t.Fatal("private request without extensions accepted")
	}
}
