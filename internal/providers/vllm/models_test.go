package vllm

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels_KeepsMaxModelLenAsContextWindow(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[
		{"id":"meta-llama/Llama-3.1-8B-Instruct","object":"model","created":1715000000,"owned_by":"vllm",
		 "root":"meta-llama/Llama-3.1-8B-Instruct","parent":null,"max_model_len":131072,"permission":[]},
		{"id":"legacy","owned_by":"vllm"}
	]}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/models", capture.Last(t).Path)
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 2)

	served := resp.Data[0]
	assert.Equal(t, "meta-llama/Llama-3.1-8B-Instruct", served.ID)
	assert.Equal(t, "model", served.Object)
	assert.Equal(t, int64(1715000000), served.Created)
	require.NotNil(t, served.Metadata)
	require.NotNil(t, served.Metadata.ContextWindow)
	assert.Equal(t, 131072, *served.Metadata.ContextWindow)
	assert.Empty(t, served.Metadata.Modes, "modes stay unset for the ID heuristic")

	legacy := resp.Data[1]
	assert.Equal(t, "model", legacy.Object, "a missing object is normalized like the plain listing")
	assert.Nil(t, legacy.Metadata, "a server that omits max_model_len reports no metadata")
}
