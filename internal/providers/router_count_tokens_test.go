package providers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTokenCountingProvider struct {
	*mockProvider
	count     int
	lastModel string
	lastBody  []byte
}

func (m *mockTokenCountingProvider) CountMessagesTokens(_ context.Context, model string, body []byte) (int, error) {
	m.lastModel, m.lastBody = model, body
	if m.err != nil {
		return 0, m.err
	}
	return m.count, nil
}

// The router hands the count to the provider that owns the model, with the
// provider prefix stripped, and reports plainly when that provider has no
// count endpoint so the caller can estimate instead.
func TestRouterCountMessagesTokens(t *testing.T) {
	counter := &mockTokenCountingProvider{mockProvider: &mockProvider{name: "anthropic"}, count: 321}
	plain := &mockProvider{name: "openai"}
	lookup := newMockLookup()
	lookup.addModel("anthropic/claude-haiku-4-5", counter, "anthropic")
	lookup.addModel("openai/gpt-5-mini", plain, "openai")
	router, _ := NewRouter(lookup)

	got, err := router.CountMessagesTokens(context.Background(), "anthropic/claude-haiku-4-5", []byte(`{"messages":[]}`))
	require.NoError(t, err)
	assert.Equal(t, 321, got)
	assert.Equal(t, "claude-haiku-4-5", counter.lastModel)

	_, err = router.CountMessagesTokens(context.Background(), "openai/gpt-5-mini", []byte(`{"messages":[]}`))
	assert.ErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
}

// A failure from the counting provider reaches the caller unchanged, so the
// handler can tell an upstream error from a provider without the endpoint.
func TestRouterCountMessagesTokens_PropagatesProviderError(t *testing.T) {
	upstream := errors.New("count_tokens upstream failed")
	counter := &mockTokenCountingProvider{mockProvider: &mockProvider{name: "anthropic", err: upstream}}
	lookup := newMockLookup()
	lookup.addModel("anthropic/claude-haiku-4-5", counter, "anthropic")
	router, _ := NewRouter(lookup)

	_, err := router.CountMessagesTokens(context.Background(), "anthropic/claude-haiku-4-5", []byte(`{"messages":[]}`))
	require.ErrorIs(t, err, upstream)
	require.NotErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
}

type mockChatTokenCountingProvider struct {
	*mockProvider
	count int
	last  *core.ChatRequest
}

func (m *mockChatTokenCountingProvider) CountChatTokens(_ context.Context, req *core.ChatRequest) (int, error) {
	m.last = req
	return m.count, nil
}

// A chat count reaches the provider as a completion would: with the resolved
// model and without Anthropic cache directives the provider does not accept.
func TestRouterCountChatTokens(t *testing.T) {
	counter := &mockChatTokenCountingProvider{mockProvider: &mockProvider{name: "openai"}, count: 130}
	plain := &mockProvider{name: "other"}
	lookup := newMockLookup()
	lookup.addModel("openai/gpt-6-luna", counter, "openai")
	lookup.addModel("other/model-x", plain, "other")
	router, _ := NewRouter(lookup)

	cacheControl := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral"}`)})
	ctx := core.WithRequestDialect(context.Background(), core.RequestDialectAnthropicMessages)
	got, err := router.CountChatTokens(ctx, &core.ChatRequest{
		Model:    "openai/gpt-6-luna",
		Messages: []core.Message{{Role: "user", Content: []core.ContentPart{{Type: "text", Text: "hi", ExtraFields: cacheControl}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, 130, got)
	require.NotNil(t, counter.last)
	assert.Equal(t, "gpt-6-luna", counter.last.Model)
	parts := counter.last.Messages[0].Content.([]core.ContentPart)
	assert.True(t, parts[0].ExtraFields.IsEmpty(), "cache_control must not reach OpenAI")

	_, err = router.CountChatTokens(ctx, &core.ChatRequest{Model: "other/model-x", Messages: []core.Message{{Role: "user", Content: "hi"}}})
	assert.ErrorIs(t, err, core.ErrMessagesTokenCountUnsupported)
}
