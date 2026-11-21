package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRequestControlHeaders(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{name: "none", names: nil, want: nil},
		{name: "blank entries", names: []string{"", "  "}, want: nil},
		{name: "any case, trimmed, once", names: []string{" x-gomodel-failover ", "X-GOMODEL-FAILOVER"}, want: []string{FailoverControlHeader}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRequestControlHeaders(tt.names)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := NormalizeRequestControlHeaders([]string{"X-GoModel-Failover", "X-Cache-TTL"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"X-Cache-TTL"`)
	assert.Contains(t, err.Error(), FailoverControlHeader)
}

func TestIsRequestControlHeader(t *testing.T) {
	assert.True(t, IsRequestControlHeader("X-Gomodel-Failover"))
	assert.True(t, IsRequestControlHeader("x-gomodel-failover"))
	assert.False(t, IsRequestControlHeader("X-GoModel-User-Path"))
	assert.False(t, IsRequestControlHeader(""))
}

func TestRequestControlsFromContext(t *testing.T) {
	assert.Equal(t, RequestControls{}, RequestControlsFromContext(context.Background()))

	ctx := WithRequestControls(context.Background(), RequestControls{FailoverOff: true})
	assert.True(t, RequestControlsFromContext(ctx).FailoverOff)
}
