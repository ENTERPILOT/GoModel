package providers

import (
	"context"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

type mockModerationProvider struct {
	*mockProvider
	lastReq *core.ModerationRequest
}

func (m *mockModerationProvider) CreateModeration(_ context.Context, req *core.ModerationRequest) (*core.ModerationResponse, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return &core.ModerationResponse{ID: "modr-1", Model: req.Model, Results: []core.ModerationResult{{Flagged: true}}}, nil
}

func TestRouterCreateModeration(t *testing.T) {
	moderator := &mockModerationProvider{mockProvider: &mockProvider{name: "openai"}}
	lookup := newMockLookup()
	lookup.addModel("openai/omni-moderation-latest", moderator, "openai")
	router, err := NewRouter(lookup)
	require.NoError(t, err)

	req := &core.ModerationRequest{Model: "omni-moderation-latest", Provider: "openai", Input: json.RawMessage(`"hi"`)}
	resp, err := router.CreateModeration(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "openai", resp.Provider)
	require.NotNil(t, moderator.lastReq)
	assert.Equal(t, "omni-moderation-latest", moderator.lastReq.Model)
	assert.Empty(t, moderator.lastReq.Provider)
	assert.Equal(t, "openai", req.Provider, "the caller's request is not mutated")
}

func TestRouterCreateModeration_UnsupportedProvider(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("anthropic/claude-sonnet-4", &mockProvider{name: "anthropic"}, "anthropic")
	router, err := NewRouter(lookup)
	require.NoError(t, err)

	_, err = router.CreateModeration(context.Background(), &core.ModerationRequest{Model: "claude-sonnet-4", Provider: "anthropic", Input: json.RawMessage(`"hi"`)})
	require.Error(t, err)
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
	assert.Contains(t, gwErr.Message, "does not support moderations")

	_, err = router.CreateModeration(context.Background(), nil)
	require.Error(t, err)
}
