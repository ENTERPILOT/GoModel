package groq

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModels_KeepsGroqMetadata(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[
		{"id":"openai/gpt-oss-20b","object":"model","created":1754407957,"owned_by":"OpenAI","active":true,
		 "context_window":131072,"max_completion_tokens":65536,"name":"GPT OSS 20B",
		 "input_modalities":["text"],"output_modalities":["text"],
		 "supported_features":["tools","json_mode","structured_outputs","reasoning"],
		 "pricing":{"prompt":"0.000000075","completion":"0.0000003","image":"0","request":"0","input_cache_read":"0.0000000375"}},
		{"id":"qwen/qwen3.8-27b","object":"model","created":1786984846,"owned_by":"Alibaba Cloud","active":true,
		 "context_window":131042,"max_completion_tokens":16384,"name":"Qwen/Qwen3.8-27B",
		 "input_modalities":["text","image"],"output_modalities":["text"],"supported_features":["tools"],
		 "pricing":{"prompt":"0.0000008","completion":"0.000004"}},
		{"id":"canopylabs/orpheus-v1-english","object":"model","created":1,"owned_by":"Canopy Labs","active":true,
		 "context_window":4000,"max_completion_tokens":50000,"name":"Canopy Labs Orpheus V1 English",
		 "input_modalities":["text"],"output_modalities":["speech"],"supported_features":null,
		 "pricing":{"prompt":"0.000022","image":"0","request":"0","input_cache_read":"0.000011"}},
		{"id":"whisper-large-v3","object":"model","created":1,"owned_by":"OpenAI","active":true,
		 "context_window":448,"max_completion_tokens":448,"name":"Whisper",
		 "input_modalities":["audio"],"output_modalities":["transcription"],"supported_features":null,"pricing":null},
		{"id":"bare","owned_by":"x"},
		{"id":" "}
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

	chat := byID["openai/gpt-oss-20b"]
	assert.Equal(t, "model", chat.Object)
	assert.Equal(t, "OpenAI", chat.OwnedBy)
	require.NotNil(t, chat.Metadata)
	assert.Equal(t, "GPT OSS 20B", chat.Metadata.DisplayName)
	assert.Equal(t, []string{"chat"}, chat.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration}, chat.Metadata.Categories)
	require.NotNil(t, chat.Metadata.ContextWindow)
	assert.Equal(t, 131072, *chat.Metadata.ContextWindow)
	require.NotNil(t, chat.Metadata.MaxOutputTokens)
	assert.Equal(t, 65536, *chat.Metadata.MaxOutputTokens)
	assert.Equal(t, map[string]bool{
		"function_calling":  true,
		"json_mode":         true,
		"structured_output": true,
		"reasoning":         true,
	}, chat.Metadata.Capabilities)
	require.NotNil(t, chat.Metadata.Pricing)
	assert.Equal(t, "USD", chat.Metadata.Pricing.Currency)
	require.NotNil(t, chat.Metadata.Pricing.InputPerMtok)
	assert.InDelta(t, 0.075, *chat.Metadata.Pricing.InputPerMtok, 1e-9)
	require.NotNil(t, chat.Metadata.Pricing.OutputPerMtok)
	assert.InDelta(t, 0.3, *chat.Metadata.Pricing.OutputPerMtok, 1e-9)
	require.NotNil(t, chat.Metadata.Pricing.CachedInputPerMtok)
	assert.InDelta(t, 0.0375, *chat.Metadata.Pricing.CachedInputPerMtok, 1e-9)

	vision := byID["qwen/qwen3.8-27b"]
	require.NotNil(t, vision.Metadata)
	assert.True(t, vision.Metadata.Capabilities["vision"], "an image input modality is a vision capability")
	assert.True(t, vision.Metadata.Capabilities["function_calling"])
	require.NotNil(t, vision.Metadata.Pricing)
	assert.Nil(t, vision.Metadata.Pricing.CachedInputPerMtok, "a missing rate is not reported as zero")

	speech := byID["canopylabs/orpheus-v1-english"]
	require.NotNil(t, speech.Metadata)
	assert.Equal(t, []string{"audio_speech"}, speech.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryAudio}, speech.Metadata.Categories)
	assert.Nil(t, speech.Metadata.Capabilities)
	require.NotNil(t, speech.Metadata.Pricing)
	assert.Nil(t, speech.Metadata.Pricing.InputPerMtok, "speech models are not priced per token")
	require.NotNil(t, speech.Metadata.Pricing.PerCharacterInput)
	assert.InDelta(t, 0.000022, *speech.Metadata.Pricing.PerCharacterInput, 1e-12)

	stt := byID["whisper-large-v3"]
	require.NotNil(t, stt.Metadata)
	assert.Equal(t, []string{"audio_transcription"}, stt.Metadata.Modes)
	assert.True(t, stt.Metadata.Capabilities["audio_input"])
	assert.Nil(t, stt.Metadata.Pricing)

	bare := byID["bare"]
	assert.Equal(t, "model", bare.Object)
	assert.Nil(t, bare.Metadata, "an entry with nothing to say carries no metadata")
}
