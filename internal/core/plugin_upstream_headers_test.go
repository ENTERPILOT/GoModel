package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPluginUpstreamDeniedHeaderLists pins one case per denied header. The
// length checks tie the tables to the lists, so a header added to one of them
// without a case here fails.
func TestPluginUpstreamDeniedHeaderLists(t *testing.T) {
	transport := []string{
		"Host",
		"Content-Length",
		"Transfer-Encoding",
		"Connection",
		"Keep-Alive",
		"Proxy-Connection",
		"Proxy-Authenticate",
		"Te",
		"Trailer",
		"Upgrade",
		"Expect",
		"Content-Type",
		"Content-Encoding",
		"Accept-Encoding",
	}
	reserved := []string{
		"Anthropic-Beta",
		"Idempotency-Key",
	}
	require.Len(t, pluginUpstreamTransportHeaders, len(transport), "transport list and cases drifted apart")
	require.Len(t, pluginUpstreamReservedHeaders, len(reserved), "reserved list and cases drifted apart")

	for _, name := range slices.Concat(transport, reserved) {
		for _, variant := range []string{name, strings.ToLower(name), " " + name + " "} {
			assert.True(t, IsPluginUpstreamHeaderDenied(variant), "IsPluginUpstreamHeaderDenied(%q)", variant)
		}
	}
}

func TestIsPluginUpstreamHeaderDenied(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "authorization", want: true},
		{name: "X-Goog-Api-Key", want: true},
		{name: "anthropic-beta", want: true},
		{name: "content-type", want: true},
		{name: "Helicone-Property-Tenant", want: false},
		{name: "X-Tenant", want: false},
		{name: "Anthropic-Version", want: false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, IsPluginUpstreamHeaderDenied(tt.name), "IsPluginUpstreamHeaderDenied(%q)", tt.name)
	}
}
