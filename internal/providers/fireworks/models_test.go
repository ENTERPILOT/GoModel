package fireworks

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

func TestListModels_KeepsFireworksMetadata(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[
		{"id":"accounts/fireworks/models/kimi-k2p6","object":"model","owned_by":"fireworks","created":1776455694,
		 "kind":"HF_BASE_MODEL","supports_chat":true,"supports_image_input":true,"supports_tools":true,"context_length":262144},
		{"id":"accounts/fireworks/models/qwen3-embedding-8b","object":"model","owned_by":"fireworks","created":1755707090,
		 "kind":"EMBEDDING_MODEL","supports_chat":true,"supports_image_input":false,"supports_tools":false,"context_length":40960},
		{"id":"accounts/fireworks/models/qwen3-reranker-8b","object":"model","owned_by":"fireworks","created":1759865045,
		 "kind":"EMBEDDING_MODEL","supports_chat":true,"supports_tools":false,"context_length":40960},
		{"id":"accounts/fireworks/models/qwen3p7-plus","object":"model","owned_by":"fireworks","created":1781036419,
		 "kind":"CUSTOM_MODEL","supports_chat":true,"supports_image_input":true,"supports_tools":true},
		{"id":"accounts/fireworks/models/bare","object":"model","owned_by":"fireworks"},
		{"id":"  ","object":"model"}
	]}`)
	provider := newTestProvider("fw_test", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Bearer fw_test", req.Header.Get("Authorization"))
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 5, "blank IDs are dropped")

	byID := make(map[string]core.Model, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	chat := byID["accounts/fireworks/models/kimi-k2p6"]
	assert.Equal(t, "model", chat.Object)
	assert.Equal(t, int64(1776455694), chat.Created)
	require.NotNil(t, chat.Metadata)
	assert.Equal(t, []string{"chat"}, chat.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration}, chat.Metadata.Categories)
	require.NotNil(t, chat.Metadata.ContextWindow)
	assert.Equal(t, 262144, *chat.Metadata.ContextWindow)
	assert.Equal(t, map[string]bool{"function_calling": true, "vision": true}, chat.Metadata.Capabilities)

	embed := byID["accounts/fireworks/models/qwen3-embedding-8b"]
	require.NotNil(t, embed.Metadata)
	assert.Equal(t, []string{"embedding"}, embed.Metadata.Modes, "kind wins over supports_chat")
	assert.Nil(t, embed.Metadata.Capabilities)

	rerank := byID["accounts/fireworks/models/qwen3-reranker-8b"]
	require.NotNil(t, rerank.Metadata)
	assert.Equal(t, []string{"rerank"}, rerank.Metadata.Modes)

	custom := byID["accounts/fireworks/models/qwen3p7-plus"]
	require.NotNil(t, custom.Metadata)
	assert.Nil(t, custom.Metadata.ContextWindow, "a missing context length is not reported as zero")
	assert.True(t, custom.Metadata.Capabilities["vision"])

	assert.Nil(t, byID["accounts/fireworks/models/bare"].Metadata, "an entry with nothing to say carries no metadata")
}
