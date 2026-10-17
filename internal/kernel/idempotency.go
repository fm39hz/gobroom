package kernel

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

// IdempotencyPolicy is a provider-operation guarantee, not a retry preference.
// MinRetentionSeconds must be backed by the upstream's idempotency contract.
type IdempotencyPolicy struct {
	Header              string `json:"header"`
	MinRetentionSeconds int64  `json:"minRetentionSeconds"`
}

func (p IdempotencyPolicy) Validate() error {
	if !httpguts.ValidHeaderFieldName(p.Header) {
		return fmt.Errorf("idempotency policy has an invalid header name")
	}
	switch strings.ToLower(p.Header) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length", "content-type", "transfer-encoding", "connection", "trailer", "upgrade", "te", "keep-alive":
		return fmt.Errorf("idempotency policy cannot replace a credential or transport header")
	}
	if p.MinRetentionSeconds <= 0 || p.MinRetentionSeconds > int64((1<<63-1)/time.Second) {
		return fmt.Errorf("idempotency policy requires a positive, bounded minimum retention")
	}
	return nil
}

func (p IdempotencyPolicy) permitsReplay(start, now time.Time) bool {
	return p.Validate() == nil && !start.IsZero() && !now.Before(start) && now.Sub(start) < time.Duration(p.MinRetentionSeconds)*time.Second
}
