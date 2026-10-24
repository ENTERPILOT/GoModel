package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// decisionsMockProvider is a mockProvider that also answers Decisions.
type decisionsMockProvider struct {
	mockProvider
	lastDecisions *core.PassthroughRequest
}

func (m *decisionsMockProvider) Decisions(_ context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	m.lastDecisions = req
	return &core.PassthroughResponse{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"answers":[]}`))}, nil
}

func TestRouterDecisions_RoutesByProviderType(t *testing.T) {
	openai := &decisionsMockProvider{name: "openai"}
	kev := &decisionsMockProvider{name: "kev"}
	lookup := newMockLookup()
	lookup.addModel("gpt-6-luna", openai, "openai")
	lookup.addModel("kev-latest", kev, "jev")
	lookup.addModel("claude-sonnet-4-6", &mockProvider{name: "anthropic"}, "anthropic")
	router, err := NewRouter(lookup)
	require.NoError(t, err)

	resp, err := router.Decisions(context.Background(), "openai", &core.PassthroughRequest{Method: http.MethodPost, Endpoint: "decisions"})
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.NotNil(t, openai.lastDecisions)
	assert.Equal(t, "decisions", openai.lastDecisions.Endpoint)

	assert.ElementsMatch(t, []string{"openai", "jev"}, router.DecisionsProviderTypes())
}

func TestRouterDecisions_RejectsProvidersWithoutDecisions(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("claude-sonnet-4-6", &mockProvider{name: "anthropic"}, "anthropic")
	router, err := NewRouter(lookup)
	require.NoError(t, err)

	_, err = router.Decisions(context.Background(), "anthropic", &core.PassthroughRequest{Method: http.MethodPost})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic does not support the decisions API")
	assert.Empty(t, router.DecisionsProviderTypes())
}
