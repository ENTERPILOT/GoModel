package plugins

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/pluginapi"
)

func TestFinishKeepsPluginUpstreamHeaders(t *testing.T) {
	state := NewRequestState()
	x := state.NewExchange(context.Background(), pluginapi.Meta{})
	x.Headers.Upstream = http.Header{
		"X-Edge-Proxy-Token": {"edge-token"},
	}

	state.Finish(x)

	assert.Equal(t, http.Header{"X-Edge-Proxy-Token": {"edge-token"}}, state.UpstreamHeaders())
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

func TestUpstreamHeadersNilWithoutPlugins(t *testing.T) {
	state := NewRequestState()
	assert.Nil(t, state.UpstreamHeaders())
}
