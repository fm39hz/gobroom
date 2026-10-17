package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
)

func TestPreparedBytesReplayFreezesPayloadAndRenewsBodyBoundAuthorization(t *testing.T) {
	payload := []byte(`{"model":"original"}`)
	headers := http.Header{"Content-Type": {"application/json"}, "X-Fixed": {"original"}}
	ctx := withPreparedIdempotency(context.Background(), "X-Test-Idempotency", "issuer-derived-key")
	authorize := func(_ context.Context, headers http.Header, body io.Reader, credential Credential) error {
		hash := sha256.New()
		_, err := io.Copy(hash, body)
		_, _ = io.WriteString(hash, headers.Get("X-Test-Idempotency"))
		headers.Set("X-Body-Signature", credential.Secret+":"+hex.EncodeToString(hash.Sum(nil)))
		return err
	}
	first, err := PrepareBytesRequest(ctx, http.MethodPost, "/jobs", headers, payload, Credential{Secret: "old"}, authorize, true)
	if err != nil {
		t.Fatal(err)
	}
	original, err := io.ReadAll(first.Body)
	if err != nil {
		t.Fatal(err)
	}
	payload[0] = '['
	headers.Set("X-Fixed", "changed")
	first.Headers.Set("X-Fixed", "transport mutation")
	replayed, err := first.Replay(ctx, Credential{Secret: "new"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(replayed.Body)
	if err != nil || string(body) != string(original) || replayed.Headers.Get("X-Fixed") != "original" {
		t.Fatalf("prepared body/headers were not frozen: body=%q headers=%v err=%v", body, replayed.Headers, err)
	}
	digest := sha256.Sum256(append(original, []byte("issuer-derived-key")...))
	if replayed.Headers.Get("X-Body-Signature") != "new:"+hex.EncodeToString(digest[:]) {
		t.Fatal("authorization did not sign the original encoded bytes with fresh credentials")
	}
	if replayed.Headers.Get("X-Test-Idempotency") != "issuer-derived-key" {
		t.Fatal("authorization did not observe the final kernel-owned dispatch header")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := first.Replay(cancelled, Credential{}); err != context.Canceled {
		t.Fatalf("cancelled replay preparation returned %v", err)
	}
}
