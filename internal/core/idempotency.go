package core

import (
	"context"
	"strings"
)

// IdempotencyKeyHeader is the request header a client uses to mark retries of
// one logical request, spelled in canonical textproto form.
const IdempotencyKeyHeader = "Idempotency-Key"

// maxIdempotencyKeyLength bounds a forwarded Idempotency-Key. Providers that
// honor the header accept keys of this size; longer values are ignored.
const maxIdempotencyKeyLength = 255

const idempotencyKeyKey contextKey = "idempotency-key"

// WithIdempotencyKey overrides the idempotency key for provider calls made
// with ctx. An empty key clears it, which is how an attempt that sends a
// different request body (a failover to another model) opts out.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKeyKey, key)
}

// IdempotencyKey returns the idempotency key for provider calls made with ctx:
// an override set with WithIdempotencyKey, otherwise the client's
// Idempotency-Key header captured in the request snapshot. A header value
// that is not a plain token of at most 255 visible ASCII characters is
// ignored, so it is safe to forward as-is.
func IdempotencyKey(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if key, ok := ctx.Value(idempotencyKeyKey).(string); ok {
		return key
	}
	// Snapshot headers are cloned from the inbound http.Header, whose keys
	// net/http has already canonicalized.
	// A repeated header is ambiguous, so it forwards nothing.
	values := GetRequestSnapshot(ctx).HeadersView()[IdempotencyKeyHeader]
	if len(values) != 1 {
		return ""
	}
	return validIdempotencyKey(values[0])
}

func validIdempotencyKey(value string) string {
	key := strings.TrimSpace(value)
	if len(key) > maxIdempotencyKeyLength {
		return ""
	}
	for i := 0; i < len(key); i++ {
		if key[i] <= ' ' || key[i] > '~' {
			return ""
		}
	}
	return key
}
