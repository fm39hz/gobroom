package kernel

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"strings"
)

// PrepareBytesRequest freezes encoded bytes only when replay is requested.
// Authorization runs after encoding and may be renewed without re-encoding.
func PrepareBytesRequest(ctx context.Context, method, url string, headers http.Header, body []byte, credential Credential, authorize PreparedAuthorization, replay bool) (UpstreamRequest, error) {
	prepare := func(reader, signingReader io.Reader, template http.Header, ctx context.Context, credential Credential) (UpstreamRequest, error) {
		if err := ctx.Err(); err != nil {
			return UpstreamRequest{}, err
		}
		h := template.Clone()
		if h == nil {
			h = make(http.Header)
		}
		ApplyPreparedRequestHeaders(ctx, h)
		if authorize != nil {
			if err := authorize(ctx, h, signingReader, credential); err != nil {
				return UpstreamRequest{}, err
			}
		}
		if err := ctx.Err(); err != nil {
			return UpstreamRequest{}, err
		}
		return UpstreamRequest{Method: method, URL: url, Headers: h, Body: reader}, nil
	}
	if !replay {
		return prepare(bytes.NewReader(body), bytes.NewReader(body), headers, ctx, credential)
	}
	// An immutable string prevents later caller mutation of the input slice.
	frozen := string(body)
	template := headers.Clone()
	var factory PreparedRequestReplay
	factory = func(ctx context.Context, credential Credential) (UpstreamRequest, error) {
		request, err := prepare(strings.NewReader(frozen), strings.NewReader(frozen), template, ctx, credential)
		request.Replay = factory
		return request, err
	}
	return factory(ctx, credential)
}

type preparedIdempotencyContextKey struct{}

type preparedIdempotency struct {
	header string
	key    string
}

func withPreparedIdempotency(ctx context.Context, header, key string) context.Context {
	return context.WithValue(ctx, preparedIdempotencyContextKey{}, preparedIdempotency{header: header, key: key})
}

// ApplyPreparedRequestHeaders presents kernel-owned dispatch metadata before
// authorization so header-bound signatures cover the actual idempotency key.
func ApplyPreparedRequestHeaders(ctx context.Context, headers http.Header) {
	if metadata, ok := ctx.Value(preparedIdempotencyContextKey{}).(preparedIdempotency); ok {
		headers.Set(metadata.header, metadata.key)
	}
}

func CredentialAuthorization(presentation CredentialPresentation) PreparedAuthorization {
	// Freeze the presentation's header maps rather than retaining caller state.
	presentation.Headers = maps.Clone(presentation.Headers)
	return func(_ context.Context, headers http.Header, _ io.Reader, credential Credential) error {
		return ApplyCredentialPresentation(headers, credential, presentation)
	}
}
