package chutes

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels_PreservesChutesMetadata(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"object":"chutes-model-catalog",
		"data":[{
			"id":"Qwen/Qwen3.5-397B-A17B-TEE",
			"owned_by":"sglang",
			"created":1677652288,
			"context_length":262144,
			"max_output_length":65536,
			"input_modalities":["text","image"],
			"supported_features":["json_mode","tools","structured_outputs","reasoning"],
			"confidential_compute":true,
			"pricing":{"prompt":0.45,"completion":3.0,"input_cache_read":0.045}
		}]
	}`)

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Bearer cpk_test", req.Header.Get("Authorization"))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "list", resp.Object)

	model := resp.Data[0]
	assert.Equal(t, "model", model.Object)
	require.NotNil(t, model.Metadata)
	require.NotNil(t, model.Metadata.ContextWindow)
	assert.Equal(t, 262144, *model.Metadata.ContextWindow)
	require.NotNil(t, model.Metadata.MaxOutputTokens)
	assert.Equal(t, 65536, *model.Metadata.MaxOutputTokens)
	assert.True(t, model.Metadata.Capabilities["tools"])
	assert.True(t, model.Metadata.Capabilities["vision"])
	assert.True(t, model.Metadata.Capabilities["confidential_compute"])

	pricing := model.Metadata.Pricing
	require.NotNil(t, pricing)
	assert.Equal(t, "USD", pricing.Currency)
	require.NotNil(t, pricing.InputPerMtok)
	assert.Equal(t, 0.45, *pricing.InputPerMtok)
	require.NotNil(t, pricing.OutputPerMtok)
	assert.Equal(t, 3.0, *pricing.OutputPerMtok)
	require.NotNil(t, pricing.CachedInputPerMtok)
	assert.Equal(t, 0.045, *pricing.CachedInputPerMtok)
}

func TestListModels_FiltersBlankIDsAndKeepsMinimalModels(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"data":[
			{"id":"   "},
			{"id":" minimal-model ","object":"model","owned_by":" chutes ","pricing":{}}
		]
	}`)

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)

	model := resp.Data[0]
	assert.Equal(t, "minimal-model", model.ID)
	assert.Equal(t, "model", model.Object)
	assert.Equal(t, "chutes", model.OwnedBy)
	assert.Nil(t, model.Metadata.ContextWindow)
	assert.Nil(t, model.Metadata.MaxOutputTokens)
	assert.Nil(t, model.Metadata.Capabilities)
	assert.Nil(t, model.Metadata.Pricing)
}

func TestListModels_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusServiceUnavailable, `{"error":{"message":"catalog unavailable"}}`)

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ListModels(context.Background())
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusServiceUnavailable, gatewayErr.StatusCode)
}

func TestModelCapabilities_MapsOptionalModalities(t *testing.T) {
	capabilities := modelCapabilities(modelInfo{
		SupportedFeatures: []string{" JSON_Mode ", "   "},
		InputModalities:   []string{"audio", "video", "unknown"},
	})
	assert.True(t, capabilities["json_mode"])
	assert.True(t, capabilities["audio"])
	assert.True(t, capabilities["video"])
}

func TestModelPricing_HandlesNilAndPartialPrices(t *testing.T) {
	var absent *modelPricing
	assert.Nil(t, absent.toCore())

	prompt := 0.25
	got := (&modelPricing{Prompt: &prompt}).toCore()
	require.NotNil(t, got)
	require.NotNil(t, got.InputPerMtok)
	assert.Equal(t, prompt, *got.InputPerMtok)
	assert.Nil(t, got.OutputPerMtok)
	assert.Nil(t, got.CachedInputPerMtok)
}
