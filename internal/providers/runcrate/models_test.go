package runcrate

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

// modelsFixture mirrors Runcrate's /v1/models listing: an OpenAI envelope whose
// entries carry output modalities, a context length, and per-million-token
// prices beside the per-token strings.
const modelsFixture = `{"object":"list","data":[
	{"id":"deepseek-ai/DeepSeek-V4.1-Flash","object":"model","created":1791228381,"owned_by":"deepseek-ai",
		"name":" DeepSeek V4.1 Flash ","context_length":1048576,"input_modalities":["text","image"],
		"output_modalities":["text"],
		"pricing":{"prompt":"0.00000014","completion":"0.00000042","input":0.147,"output":0.441}},
	{"id":"Qwen/Qwen3-Embedding-0.6B","object":"model","owned_by":"Qwen","name":"Qwen3 Embedding 0.6B",
		"context_length":32768,"output_modalities":["embeddings"],"pricing":{"input":0.01,"output":0}},
	{"id":"openai/gpt-oss-120b","object":"model","output_modalities":["text"],"pricing":{"input":0,"output":0}},
	{"id":"black-forest-labs/FLUX.2-dev","object":"model","output_modalities":["image"]},
	{"id":"example/video-model","object":"model","output_modalities":["video"]},
	{"id":"example/speech-model","object":"model","output_modalities":["audio"]},
	{"id":"   ","output_modalities":["text"]},
	{"id":"untyped/model"}
]}`

func listModels(t *testing.T, body string) map[string]core.Model {
	t.Helper()
	server, capture := providertest.JSONServer(t, http.StatusOK, body)
	provider := newTestProvider("runcrate-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "list", resp.Object)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Bearer runcrate-key", req.Header.Get("Authorization"))

	byID := make(map[string]core.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	return byID
}

func TestListModels_KeepsOnlyServableModalities(t *testing.T) {
	models := listModels(t, modelsFixture)

	assert.ElementsMatch(t, []string{
		"deepseek-ai/DeepSeek-V4.1-Flash",
		"Qwen/Qwen3-Embedding-0.6B",
		"openai/gpt-oss-120b",
		"untyped/model",
	}, keys(models))
}

func TestListModels_NormalizesMetadata(t *testing.T) {
	models := listModels(t, modelsFixture)
	ptr := func(v float64) *float64 { return &v }

	tests := []struct {
		id          string
		ownedBy     string
		displayName string
		modes       []string
		context     *int
		pricing     *core.ModelPricing
	}{
		{
			id:          "deepseek-ai/DeepSeek-V4.1-Flash",
			ownedBy:     "deepseek-ai",
			displayName: "DeepSeek V4.1 Flash",
			modes:       []string{"chat", "responses"},
			context:     new(1048576),
			pricing:     &core.ModelPricing{Currency: "USD", InputPerMtok: ptr(0.147), OutputPerMtok: ptr(0.441)},
		},
		{
			id:          "Qwen/Qwen3-Embedding-0.6B",
			ownedBy:     "Qwen",
			displayName: "Qwen3 Embedding 0.6B",
			modes:       []string{"embedding"},
			context:     new(32768),
			pricing:     &core.ModelPricing{Currency: "USD", InputPerMtok: ptr(0.01), OutputPerMtok: ptr(0)},
		},
		{
			id:    "openai/gpt-oss-120b",
			modes: []string{"chat", "responses"},
		},
		{id: "untyped/model"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			model, ok := models[tt.id]
			require.True(t, ok)
			require.NotNil(t, model.Metadata)
			assert.Equal(t, "model", model.Object)
			assert.Equal(t, tt.ownedBy, model.OwnedBy)
			assert.Equal(t, tt.displayName, model.Metadata.DisplayName)
			assert.Equal(t, tt.modes, model.Metadata.Modes)
			assert.Equal(t, tt.context, model.Metadata.ContextWindow)
			assert.Equal(t, tt.pricing, model.Metadata.Pricing)
		})
	}
}

func TestListModels_MarksMissingEndpointUnsupported(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusNotFound, `{"error":{"message":"not found"}}`)
	provider := newTestProvider("runcrate-key", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.ListModels(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModelListingUnsupported)
}

func keys(models map[string]core.Model) []string {
	out := make([]string, 0, len(models))
	for id := range models {
		out = append(out, id)
	}
	return out
}
