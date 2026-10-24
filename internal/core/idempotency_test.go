package core

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func contextWithInboundIdempotencyKey(value string) context.Context {
	header := http.Header{}
	header.Set("idempotency-key", value)
	snapshot := NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, header, "application/json", nil, false, "req-id", nil)
	return WithRequestSnapshot(context.Background(), snapshot)
}

func TestIdempotencyKey_FromRequestSnapshot(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain token", value: "req-123", want: "req-123"},
		{name: "uuid with surrounding space", value: "  3f1c2d9e-8a44-4b5e-9c1a-2f6b7d8e9a01 ", want: "3f1c2d9e-8a44-4b5e-9c1a-2f6b7d8e9a01"},
		{name: "longest accepted", value: strings.Repeat("k", maxIdempotencyKeyLength), want: strings.Repeat("k", maxIdempotencyKeyLength)},
		{name: "too long", value: strings.Repeat("k", maxIdempotencyKeyLength+1)},
		{name: "inner space", value: "req 123"},
		{name: "control character", value: "req\x01123"},
		{name: "non-ASCII", value: "clé-123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IdempotencyKey(contextWithInboundIdempotencyKey(tt.value)))
		})
	}
}

func TestIdempotencyKey_RepeatedHeaderForwardsNothing(t *testing.T) {
	for _, values := range [][]string{{"req-1", "req-2"}, {"", "req-2"}} {
		header := http.Header{"Idempotency-Key": values}
		snapshot := NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, header, "", nil, false, "req-id", nil)
		assert.Empty(t, IdempotencyKey(WithRequestSnapshot(context.Background(), snapshot)), "values %q", values)
	}
}

func TestIdempotencyKey_Overrides(t *testing.T) {
	assert.Empty(t, IdempotencyKey(context.Background()))
	assert.Empty(t, IdempotencyKey(WithRequestSnapshot(context.Background(), NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, nil, "", nil, false, "req-id", nil))))

	inbound := contextWithInboundIdempotencyKey("req-123")
	assert.Equal(t, "other", IdempotencyKey(WithIdempotencyKey(inbound, "other")))

	cleared := WithIdempotencyKey(inbound, "")
	assert.Empty(t, IdempotencyKey(cleared), "an empty override clears the client's key")
	assert.Equal(t, "req-123", IdempotencyKey(inbound), "clearing a derived context leaves the parent untouched")
}
