package oracle

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

func TestListModels_ReturnsUpstreamInventory(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[{"id":"openai.gpt-oss-120b","object":"model","owned_by":"oracle"}]}`)

	provider := NewWithHTTPClient("oracle-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/models", capture.Last(t).Path)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "openai.gpt-oss-120b", resp.Data[0].ID)
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	provider := NewWithHTTPClient("oracle-key", nil, llmclient.Hooks{})

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "text-embedding-3-small"})
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "oracle does not support embeddings")
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("oracle-key", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.PassthroughProvider)
	assert.False(t, ok, "provider should not implement core.PassthroughProvider")
}
