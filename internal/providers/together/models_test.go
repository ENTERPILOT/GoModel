package together

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

// modelsFixture mirrors Together's /v1/models listing: a bare JSON array whose
// entries carry a model type, context length, and per-million-token prices.
const modelsFixture = `[
	{"id":"meta-llama/Llama-3.3-70B-Instruct-Turbo","object":"model","created":1733443200,"type":"chat",
		"display_name":" Llama 3.3 70B Instruct Turbo ","organization":"Meta","context_length":131072,
		"pricing":{"hourly":0,"input":0.88,"output":0.88,"base":0,"finetune":0}},
	{"id":"BAAI/bge-base-en-v1.5","object":"model","type":"embedding","organization":"BAAI",
		"context_length":512,"pricing":{"input":0.008,"output":0}},
	{"id":"Qwen/Qwen2.5-Coder-32B-Instruct","object":"model","type":"code","pricing":{"input":0,"output":0}},
	{"id":"black-forest-labs/FLUX.1-schnell","object":"model","type":"image","pricing":{"base":0.0027}},
	{"id":"cartesia/sonic","object":"model","type":"audio"},
	{"id":"Salesforce/Llama-Rank-V1","object":"model","type":"rerank"},
	{"id":"   ","type":"chat"},
	{"id":"untyped/model"}
]`

func listModels(t *testing.T, body string) map[string]core.Model {
	t.Helper()
	server, capture := providertest.JSONServer(t, http.StatusOK, body)
	provider := newTestProvider("together-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "list", resp.Object)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Bearer together-key", req.Header.Get("Authorization"))

	byID := make(map[string]core.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	return byID
}

func TestListModels_KeepsOnlyServableTypes(t *testing.T) {
	models := listModels(t, modelsFixture)

	assert.ElementsMatch(t, []string{
		"meta-llama/Llama-3.3-70B-Instruct-Turbo",
		"BAAI/bge-base-en-v1.5",
		"Qwen/Qwen2.5-Coder-32B-Instruct",
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
			id:          "meta-llama/Llama-3.3-70B-Instruct-Turbo",
			ownedBy:     "Meta",
			displayName: "Llama 3.3 70B Instruct Turbo",
			modes:       []string{"chat", "responses"},
			context:     new(131072),
			pricing:     &core.ModelPricing{Currency: "USD", InputPerMtok: ptr(0.88), OutputPerMtok: ptr(0.88)},
		},
		{
			id:      "BAAI/bge-base-en-v1.5",
			ownedBy: "BAAI",
			modes:   []string{"embedding"},
			context: new(512),
			pricing: &core.ModelPricing{Currency: "USD", InputPerMtok: ptr(0.008), OutputPerMtok: ptr(0)},
		},
		{
			id:    "Qwen/Qwen2.5-Coder-32B-Instruct",
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

func TestListModels_AcceptsOpenAIEnvelope(t *testing.T) {
	models := listModels(t, `{"object":"list","data":[{"id":"openai/gpt-oss-120b","object":"model","type":"chat"}]}`)

	require.Contains(t, models, "openai/gpt-oss-120b")
	assert.Equal(t, []string{"chat", "responses"}, models["openai/gpt-oss-120b"].Metadata.Modes)
}

func TestListModels_MarksMissingEndpointUnsupported(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusNotFound, `{"error":{"message":"not found"}}`)
	provider := newTestProvider("together-key", server.URL, server.Client(), llmclient.Hooks{})

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
