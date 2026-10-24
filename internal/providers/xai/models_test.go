package xai

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels_KeepsXAIMetadata(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[
		{"id":"grok-4.5","aliases":["grok-4.5-latest"],"context_length":500000,"created":1782691200,"object":"model","owned_by":"xai",
		 "prompt_text_token_price":20000,"cached_prompt_text_token_price":3000,"prompt_image_token_price":20000,"completion_text_token_price":60000,
		 "prompt_text_token_price_long_context":40000,"cached_prompt_text_token_price_long_context":6000,"completion_text_token_price_long_context":120000,
		 "long_context_threshold":200000,
		 "capabilities":{"reasoning_effort":["low","medium","high","xhigh"],"default_reasoning_effort":"high"}},
		{"id":"grok-4.20-0309-non-reasoning","context_length":1000000,"created":1773014400,"object":"model","owned_by":"xai",
		 "prompt_text_token_price":12500,"completion_text_token_price":25000,"prompt_image_token_price":0,
		 "prompt_text_token_price_long_context":25000,"completion_text_token_price_long_context":50000,"long_context_threshold":200000},
		{"id":"grok-build-0.1","context_length":256000,"created":1776297600,"object":"model","owned_by":"xai",
		 "prompt_text_token_price":10000,"completion_text_token_price":20000,"long_context_threshold":200000},
		{"id":"grok-imagine-image","aliases":["grok-imagine-image-2026-03-02"],"context_length":16000,"created":1769558400,"object":"model","owned_by":"xai","image_price":200000000},
		{"id":"grok-imagine-video","created":1769558400,"object":"model","owned_by":"xai"},
		{"id":""}
	]}`)
	provider := newTestProvider(server.URL)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 5, "blank IDs are dropped")

	byID := make(map[string]core.Model, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	reasoning := byID["grok-4.5"]
	assert.Equal(t, "model", reasoning.Object)
	assert.Equal(t, int64(1782691200), reasoning.Created)
	require.NotNil(t, reasoning.Metadata)
	assert.Equal(t, []string{"chat", "responses"}, reasoning.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration}, reasoning.Metadata.Categories)
	require.NotNil(t, reasoning.Metadata.ContextWindow)
	assert.Equal(t, 500000, *reasoning.Metadata.ContextWindow)
	assert.Equal(t, map[string]bool{"reasoning": true, "vision": true}, reasoning.Metadata.Capabilities)

	pricing := reasoning.Metadata.Pricing
	require.NotNil(t, pricing)
	assert.Equal(t, "USD", pricing.Currency)
	// Prices are quoted in 1/10,000th of a cent per token: 20000 → $2/MTok.
	require.NotNil(t, pricing.InputPerMtok)
	assert.InDelta(t, 2.0, *pricing.InputPerMtok, 1e-9)
	require.NotNil(t, pricing.OutputPerMtok)
	assert.InDelta(t, 6.0, *pricing.OutputPerMtok, 1e-9)
	require.NotNil(t, pricing.CachedInputPerMtok)
	assert.InDelta(t, 0.3, *pricing.CachedInputPerMtok, 1e-9)
	require.Len(t, pricing.Tiers, 2, "long-context rates become a second tier")
	require.NotNil(t, pricing.Tiers[0].UpToTokens)
	assert.Equal(t, float64(200000), *pricing.Tiers[0].UpToTokens)
	assert.InDelta(t, 2.0, *pricing.Tiers[0].InputPerMtok, 1e-9)
	require.NotNil(t, pricing.Tiers[0].CachedInputPerMtok)
	assert.InDelta(t, 0.3, *pricing.Tiers[0].CachedInputPerMtok, 1e-9)
	require.NotNil(t, pricing.Tiers[1].UpToTokens)
	assert.Equal(t, float64(500000), *pricing.Tiers[1].UpToTokens)
	assert.InDelta(t, 4.0, *pricing.Tiers[1].InputPerMtok, 1e-9)
	require.NotNil(t, pricing.Tiers[1].CachedInputPerMtok, "the long-context cached rate rides on the second tier")
	assert.InDelta(t, 0.6, *pricing.Tiers[1].CachedInputPerMtok, 1e-9)
	assert.InDelta(t, 12.0, *pricing.Tiers[1].OutputPerMtok, 1e-9)

	plain := byID["grok-4.20-0309-non-reasoning"]
	require.NotNil(t, plain.Metadata)
	assert.Nil(t, plain.Metadata.Capabilities, "no reasoning effort and a zero image rate claim nothing")
	require.NotNil(t, plain.Metadata.Pricing)
	assert.Nil(t, plain.Metadata.Pricing.CachedInputPerMtok, "a missing rate is not reported as zero")
	require.Len(t, plain.Metadata.Pricing.Tiers, 2)
	assert.Nil(t, plain.Metadata.Pricing.Tiers[1].CachedInputPerMtok)

	build := byID["grok-build-0.1"]
	require.NotNil(t, build.Metadata)
	require.NotNil(t, build.Metadata.Pricing)
	assert.Empty(t, build.Metadata.Pricing.Tiers, "a threshold without long-context rates adds no tier")

	image := byID["grok-imagine-image"]
	require.NotNil(t, image.Metadata)
	assert.Equal(t, []string{"image_generation"}, image.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryImage}, image.Metadata.Categories)
	require.NotNil(t, image.Metadata.Pricing)
	require.NotNil(t, image.Metadata.Pricing.PerImage)
	assert.InDelta(t, 0.02, *image.Metadata.Pricing.PerImage, 1e-9)
	assert.Nil(t, image.Metadata.Pricing.InputPerMtok)

	video := byID["grok-imagine-video"]
	require.NotNil(t, video.Metadata)
	assert.Equal(t, []string{"video_generation"}, video.Metadata.Modes)
	assert.Nil(t, video.Metadata.Pricing)
	assert.Nil(t, video.Metadata.ContextWindow)
}
