package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// messagesCountTokensRecorder answers a native Messages token count and keeps
// the plugin upstream headers the call carried.
type messagesCountTokensRecorder struct {
	*mockProvider
	headers []http.Header
}

func (m *messagesCountTokensRecorder) CountMessagesTokens(ctx context.Context, _ string, _ []byte) (int, error) {
	m.headers = append(m.headers, core.PluginUpstreamHeaders(ctx))
	return 4242, nil
}

// TestMessagesCountTokensCarriesPluginUpstreamHeaders proves that
// /v1/messages/count_tokens reaches a provider behind an authenticating proxy
// with the headers a prompt-phase plugin asked for, not only the inference and
// Responses utility routes.
func TestMessagesCountTokensCarriesPluginUpstreamHeaders(t *testing.T) {
	provider := &messagesCountTokensRecorder{mockProvider: &mockProvider{
		supportedModels: []string{"claude-test"},
		providerTypes:   map[string]string{"claude-test": "mock"},
	}}
	srv := New(provider, nil)

	// Stand in for the prompt chain: the state holds what a prompt-phase plugin
	// wrote to Exchange.Headers.Upstream and nothing else of the request.
	ctx, state := plugins.WithRequestState(context.Background())
	exchange := state.NewExchange(ctx, pluginapi.Meta{})
	exchange.Headers.Upstream = http.Header{"Helicone-Property-Tenant": {"acme"}}
	state.Finish(exchange)

	body := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"count these"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, provider.headers, 1, "the count never reached the provider")
	assert.Equal(t, "acme", provider.headers[0].Get("Helicone-Property-Tenant"),
		"count_tokens dropped the plugin upstream header")
}
