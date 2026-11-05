package deepinfra

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// modelsFixture mirrors DeepInfra's /v1/openai/models listing: every entry
// carries a metadata object whose first tag names the model kind.
const modelsFixture = `{"object":"list","data":[
	{"id":"Qwen/Qwen3-32B","object":"model","created":0,"owned_by":"deepinfra","metadata":{
		"description":" Qwen3 dense model. ","context_length":40960,"max_tokens":40960,
		"pricing":{"input_tokens":0.1,"output_tokens":0.3,"cache_read_tokens":0.02},
		"tags":["chat","reasoning","reasoning_effort","vision","prompt_cache"]}},
	{"id":"Qwen/Qwen3-Embedding-0.6B","object":"model","owned_by":"deepinfra","metadata":{
		"context_length":32768,"max_tokens":32768,"pricing":{"input_tokens":0.01},"tags":["embed"]}},
	{"id":"black-forest-labs/FLUX-1-schnell","object":"model","owned_by":"deepinfra","metadata":{
		"pricing":{"per_image_unit":0.0005},"tags":["image-gen"]}},
	{"id":"hexgrad/Kokoro-82M","object":"model","owned_by":"deepinfra","metadata":{
		"pricing":{"input_characters":0.8},"tags":["tts"]}},
	{"id":"openai/whisper-large-v3-turbo","object":"model","owned_by":"deepinfra","metadata":{
		"pricing":{"input_seconds":0.00000333},"tags":["stt"]}},
	{"id":"Wan-AI/Wan2.2-T2V-A14B","object":"model","owned_by":"deepinfra","metadata":{
		"pricing":{"output_seconds":0.075},"tags":["video-gen"]}},
	{"id":"   ","metadata":{"tags":["chat"]}},
	{"id":"untagged/model"}
]}`

func listFixtureModels(t *testing.T) map[string]core.Model {
	t.Helper()
	server, capture := providertest.JSONServer(t, http.StatusOK, modelsFixture)
	provider := newTestProvider("di-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "list", resp.Object)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Bearer di-key", req.Header.Get("Authorization"))

	byID := make(map[string]core.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	return byID
}

func TestListModels_NormalizesChatMetadata(t *testing.T) {
	model := listFixtureModels(t)["Qwen/Qwen3-32B"]
	require.NotNil(t, model.Metadata)

	assert.Equal(t, "deepinfra", model.OwnedBy)
	assert.Equal(t, "Qwen3 dense model.", model.Metadata.Description)
	assert.Equal(t, []string{"chat", "responses"}, model.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration}, model.Metadata.Categories)
	require.NotNil(t, model.Metadata.ContextWindow)
	assert.Equal(t, 40960, *model.Metadata.ContextWindow)
	require.NotNil(t, model.Metadata.MaxOutputTokens)
	assert.Equal(t, 40960, *model.Metadata.MaxOutputTokens)
	assert.Equal(t, map[string]bool{"reasoning": true, "vision": true, "prompt_caching": true}, model.Metadata.Capabilities)

	pricing := model.Metadata.Pricing
	require.NotNil(t, pricing)
	assert.Equal(t, "USD", pricing.Currency)
	require.NotNil(t, pricing.InputPerMtok)
	assert.Equal(t, 0.1, *pricing.InputPerMtok)
	require.NotNil(t, pricing.OutputPerMtok)
	assert.Equal(t, 0.3, *pricing.OutputPerMtok)
	require.NotNil(t, pricing.CachedInputPerMtok)
	assert.Equal(t, 0.02, *pricing.CachedInputPerMtok)
}

func TestListModels_MapsNonChatKinds(t *testing.T) {
	models := listFixtureModels(t)

	tests := []struct {
		id      string
		modes   []string
		pricing func(t *testing.T, p *core.ModelPricing)
	}{
		{
			id:    "Qwen/Qwen3-Embedding-0.6B",
			modes: []string{"embedding"},
			pricing: func(t *testing.T, p *core.ModelPricing) {
				require.NotNil(t, p.InputPerMtok)
				assert.Equal(t, 0.01, *p.InputPerMtok)
			},
		},
		{
			id:    "black-forest-labs/FLUX-1-schnell",
			modes: []string{"image_generation"},
			pricing: func(t *testing.T, p *core.ModelPricing) {
				require.NotNil(t, p.PerImage)
				assert.Equal(t, 0.0005, *p.PerImage)
			},
		},
		{
			id:    "hexgrad/Kokoro-82M",
			modes: []string{"audio_speech"},
			pricing: func(t *testing.T, p *core.ModelPricing) {
				require.NotNil(t, p.PerCharacterInput)
				assert.InDelta(t, 0.8/1_000_000, *p.PerCharacterInput, 1e-15)
			},
		},
		{
			id:    "openai/whisper-large-v3-turbo",
			modes: []string{"audio_transcription"},
			pricing: func(t *testing.T, p *core.ModelPricing) {
				require.NotNil(t, p.PerSecondInput)
				assert.Equal(t, 0.00000333, *p.PerSecondInput)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			model, ok := models[tt.id]
			require.True(t, ok, "model should be listed")
			require.NotNil(t, model.Metadata)
			assert.Equal(t, tt.modes, model.Metadata.Modes)
			assert.Equal(t, core.CategoriesForModes(tt.modes), model.Metadata.Categories)
			require.NotNil(t, model.Metadata.Pricing)
			tt.pricing(t, model.Metadata.Pricing)
		})
	}
}

func TestListModels_SkipsUnservableAndBlankModels(t *testing.T) {
	models := listFixtureModels(t)

	assert.NotContains(t, models, "Wan-AI/Wan2.2-T2V-A14B", "video-only models have no gateway surface")
	assert.NotContains(t, models, "")
	assert.Len(t, models, 6)

	untagged, ok := models["untagged/model"]
	require.True(t, ok, "a model without metadata gives no signal and is kept")
	assert.Equal(t, "model", untagged.Object)
	assert.Nil(t, untagged.Metadata)
}
