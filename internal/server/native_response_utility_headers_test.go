package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// upstreamHeaderPatcher stands in for the prompt chain: it writes a header to
// the request state the way a prompt-phase plugin does, and leaves the request
// untouched.
type upstreamHeaderPatcher struct{}

func (upstreamHeaderPatcher) PatchChatRequest(_ context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return req, nil
}

func (upstreamHeaderPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	state := plugins.RequestStateFor(ctx)
	exchange := state.NewExchange(ctx, pluginapi.Meta{})
	exchange.Headers.Upstream = http.Header{"X-Edge-Proxy-Token": {"edge-token"}}
	state.Finish(exchange)
	return req, nil
}

// TestResponsesUtilityRoutesCarryPluginUpstreamHeaders proves that token
// counting and compaction reach a provider behind an authenticating proxy with
// the headers a prompt-phase plugin asked for, not only the inference routes.
func TestResponsesUtilityRoutesCarryPluginUpstreamHeaders(t *testing.T) {
	provider := &mockProvider{
		supportedModels: []string{"gpt-5-mini"},
		providerTypes:   map[string]string{"gpt-5-mini": "mock"},
	}
	srv := New(provider, nil)
	srv.handler.translatedRequestPatcher = upstreamHeaderPatcher{}

	for _, path := range []string{"/v1/responses/input_tokens", "/v1/responses/compact"} {
		t.Run(path, func(t *testing.T) {
			ctx, _ := plugins.WithRequestState(context.Background())
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5-mini","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}

	require.Len(t, provider.capturedUtilityHeaders, 2)
	for _, headers := range provider.capturedUtilityHeaders {
		require.Equal(t, "edge-token", headers.Get("X-Edge-Proxy-Token"))
	}
}
