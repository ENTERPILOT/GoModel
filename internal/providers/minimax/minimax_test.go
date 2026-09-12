package minimax

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

func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "minimax",
		DefaultBaseURL: "https://api.minimax.io/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
		Embeddings: true,
	})
}

func TestChatCompletion_ClampsZeroTemperature(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})

	temp := 0.0
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "MiniMax-M3",
		Messages:    []core.Message{{Role: "user", Content: "hi"}},
		Temperature: &temp,
	})
	require.NoError(t, err)
	assert.Equal(t, float64(1), capture.Last(t).JSON(t)["temperature"])
}

// MiniMax serves speech natively (see audio.go), so only the batch and file
// surfaces must stay hidden.
func TestProvider_DoesNotExposeBatchOrFileInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("minimax-key", "", nil, llmclient.Hooks{})
	_, ok := any(provider).(core.NativeBatchProvider)
	require.False(t, ok)
	_, ok = any(provider).(core.NativeFileProvider)
	require.False(t, ok)
}

func TestClampTemperature_NilRequest(t *testing.T) {
	require.Nil(t, clampTemperature(nil))
}

func TestClampTemperature_NilTemperature(t *testing.T) {
	req := &core.ChatRequest{Model: "MiniMax-M3"}
	require.Nil(t, clampTemperature(req).Temperature)
}

func TestClampTemperature_ZeroTemperature(t *testing.T) {
	temp := 0.0
	req := &core.ChatRequest{Model: "MiniMax-M3", Temperature: &temp}
	result := clampTemperature(req)
	require.NotNil(t, result.Temperature)
	assert.Equal(t, defaultTemperature, *result.Temperature)

	// Original request should not be mutated
	assert.Equal(t, 0.0, *req.Temperature)
}

func TestClampTemperature_PositiveTemperature(t *testing.T) {
	temp := 0.7
	req := &core.ChatRequest{Model: "MiniMax-M3", Temperature: &temp}
	result := clampTemperature(req)
	require.Same(t, req, result)
	assert.Equal(t, 0.7, *result.Temperature)
}
