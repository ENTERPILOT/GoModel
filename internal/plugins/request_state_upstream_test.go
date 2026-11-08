package plugins

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi"
)

func TestFinishKeepsPluginUpstreamHeaders(t *testing.T) {
	state := NewRequestState()
	x := state.NewExchange(context.Background(), pluginapi.Meta{})
	x.Headers.Upstream = http.Header{
		"Helicone-Property-Tenant": {"acme"},
	}

	state.Finish(x)

	assert.Equal(t, http.Header{"Helicone-Property-Tenant": {"acme"}}, state.UpstreamHeaders())
}

func TestFinishMergesUpstreamHeadersAcrossExchanges(t *testing.T) {
	state := NewRequestState()
	first := state.NewExchange(context.Background(), pluginapi.Meta{})
	first.Headers.Upstream = http.Header{"X-First": {"1"}}
	state.Finish(first)

	second := state.NewExchange(context.Background(), pluginapi.Meta{})
	second.Headers.Upstream = http.Header{"X-Second": {"2"}}
	state.Finish(second)

	assert.Equal(t, http.Header{"X-First": {"1"}, "X-Second": {"2"}}, state.UpstreamHeaders())
}

func TestFinishCanonicalizesUpstreamHeaderNames(t *testing.T) {
	state := NewRequestState()
	first := state.NewExchange(context.Background(), pluginapi.Meta{})
	first.Headers.Upstream = http.Header{"x-tenant": {"first"}}
	state.Finish(first)

	second := state.NewExchange(context.Background(), pluginapi.Meta{})
	second.Headers.Upstream = http.Header{"X-Tenant": {"second"}}
	state.Finish(second)

	// One canonical key, and the later exchange wins regardless of map order.
	assert.Equal(t, http.Header{"X-Tenant": {"second"}}, state.UpstreamHeaders())
}

func TestFinishResolvesCaseVariantsOnOneExchangeDeterministically(t *testing.T) {
	// Readers merge raw keys back, so one exchange can carry both spellings.
	// Map iteration order varies between runs; the result must not.
	for range 50 {
		state := NewRequestState()
		x := state.NewExchange(context.Background(), pluginapi.Meta{})
		x.Headers.Upstream = http.Header{"x-tenant": {"lower"}, "X-Tenant": {"canonical"}}
		state.Finish(x)

		require.Equal(t, http.Header{"X-Tenant": {"lower"}}, state.UpstreamHeaders())
	}
}

func TestUpstreamHeadersNilWithoutPlugins(t *testing.T) {
	state := NewRequestState()
	assert.Nil(t, state.UpstreamHeaders())
}
