package audiocpp

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

// The listing is the payload audiocpp_server returns for two configured
// models: the fields beyond the OpenAI shape (family, task, mode) are what
// decide the modes the gateway routes on.
const modelsJSON = `{
  "object": "list",
  "data": [
    {"id":"moonshine-tiny","object":"model","owned_by":"engine","family":"moonshine_asr","task":"asr","mode":"streaming","loaded":false,"path":"/models/moonshine.gguf"},
    {"id":"pocket-tts","object":"model","owned_by":"engine","family":"pocket_tts","task":"tts","mode":"offline","loaded":true,"path":"/models/pocket-tts"},
    {"id":"qwen3-align","object":"model","owned_by":"engine","family":"qwen3_align","task":"align","mode":"offline","loaded":false,"path":"/models/align"}
  ]
}`

func TestListModels_MapsTaskToModeAndKeepsTheRest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, modelsJSON)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, capture.Last(t).Method)
	assert.Equal(t, "/v1/models", capture.Last(t).Path)

	require.Len(t, resp.Data, 3)
	assert.Equal(t, "list", resp.Object)

	asr := resp.Data[0]
	assert.Equal(t, "moonshine-tiny", asr.ID)
	assert.Equal(t, "model", asr.Object)
	assert.Equal(t, "engine", asr.OwnedBy)
	require.NotNil(t, asr.Metadata)
	assert.Equal(t, "moonshine_asr", asr.Metadata.Family)
	assert.Equal(t, []string{"audio_transcription"}, asr.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryAudio}, asr.Metadata.Categories)
	assert.Equal(t, map[string]bool{"streaming": true}, asr.Metadata.Capabilities)

	tts := resp.Data[1]
	assert.Equal(t, []string{"audio_speech"}, tts.Metadata.Modes)
	assert.Nil(t, tts.Metadata.Capabilities, "an offline model claims no streaming")

	// A task with no OpenAI endpoint stays listed — it is reachable through
	// native passthrough — but claims neither audio mode.
	align := resp.Data[2]
	assert.Equal(t, "qwen3-align", align.ID)
	assert.Empty(t, align.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryAudio}, align.Metadata.Categories)
}

func TestListModels_FillsMissingObjectNames(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"data":[{"id":"pocket-tts","task":"tts"}]}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "model", resp.Data[0].Object)
}

func TestListModels_PropagatesUpstreamFailure(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError,
		`{"error":{"message":"models unavailable","type":"server_error"}}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "models unavailable")
}
