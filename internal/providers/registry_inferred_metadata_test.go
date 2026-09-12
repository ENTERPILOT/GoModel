package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitialize_InfersEmbeddingModesForUnknownModels verifies the last-resort
// ID heuristic: a local model absent from the remote model registry (the
// llama.cpp / LM Studio / Ollama case) whose ID clearly names an embedding
// model is categorized as an embedding model, so the dashboard's Embeddings
// filter and category counts see it. Registry data and operator overrides
// always win over the inference.
func TestInitialize_InfersEmbeddingModesForUnknownModels(t *testing.T) {
	registry := NewModelRegistry()

	local := &registryMockProvider{
		name: "provider-lagash",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "nomic-embed-text-v1.5.Q8_0.gguf", Object: "model", OwnedBy: "llamacpp"},
				{ID: "bge-m3", Object: "model", OwnedBy: "llamacpp"},
				{ID: "llama-3.1-8b-instruct", Object: "model", OwnedBy: "llamacpp"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(local, "lagash", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	for _, id := range []string{"nomic-embed-text-v1.5.Q8_0.gguf", "bge-m3"} {
		info := registry.GetModel("lagash/" + id)
		require.NotNil(t, info)
		require.NotNil(t, info.Model.Metadata, "expected %s to have inferred metadata", id)

		meta := info.Model.Metadata
		assert.Len(t, meta.Modes, 1)
		assert.Equal(t, "embedding", meta.Modes[0], "%s Modes = %v, want [embedding]", id, meta.Modes)
		assert.Len(t, meta.Categories, 1)
		assert.Equal(t, core.CategoryEmbedding, meta.Categories[0], "%s Categories = %v, want [embedding]", id, meta.Categories)
	}

	chat := registry.GetModel("lagash/llama-3.1-8b-instruct")
	require.NotNil(t, chat)
	assert.Nil(t, chat.Model.Metadata, "chat model must not get inferred metadata")

	embeddings := registry.ListModelsWithProviderByCategory(core.CategoryEmbedding)
	found := map[string]bool{}
	for _, m := range embeddings {
		found[m.Model.ID] = true
	}
	assert.True(t, found["nomic-embed-text-v1.5.Q8_0.gguf"])
	assert.True(t, found["bge-m3"], "embedding category listing = %v, want both local embedding models", found)
	assert.False(t, found["llama-3.1-8b-instruct"], "chat model must not appear in the embedding category")
}

// TestApplyInferredModelMetadata_ReplacementsProtocol exercises the published-
// map path (EnrichModels), where entries must be replaced rather than mutated
// in place so concurrent readers keep a stable view. Covers both a fresh entry
// and one already replaced by an earlier enrichment step (reverse-chain case).
func TestApplyInferredModelMetadata_ReplacementsProtocol(t *testing.T) {
	fresh := &ModelInfo{Model: core.Model{ID: "nomic-embed-text"}, ProviderName: "eridu"}
	orig := &ModelInfo{Model: core.Model{ID: "bge-m3"}, ProviderName: "eridu"}
	// Simulate a prior pass (registry enrichment) having already replaced orig
	// with a clone that still lacks modes/categories.
	priorClone := *orig
	prior := &priorClone
	chat := &ModelInfo{Model: core.Model{ID: "some-chat-model"}, ProviderName: "eridu"}

	providerModels := map[string]*ModelInfo{
		"nomic-embed-text": fresh,
		"bge-m3":           prior,
		"some-chat-model":  chat,
	}
	replacements := map[*ModelInfo]*ModelInfo{orig: prior}

	applied := applyInferredModelMetadata(map[string]map[string]*ModelInfo{"eridu": providerModels}, replacements)
	require.Equal(t, 2, applied)

	// Original pointers must be untouched; new entries carry the metadata.
	assert.Nil(t, fresh.Model.Metadata)
	assert.Nil(t, prior.Model.Metadata)

	for _, id := range []string{"nomic-embed-text", "bge-m3"} {
		next := providerModels[id]
		assert.NotNil(t, next.Model.Metadata)
		assert.Len(t, next.Model.Metadata.Modes, 1)
		assert.Equal(t, "embedding", next.Model.Metadata.Modes[0], "%s replacement metadata = %+v, want embedding modes", id, next.Model.Metadata)
	}
	got := replacements[fresh]
	assert.Equal(t, providerModels["nomic-embed-text"], got)
	got = // The chain must point from the ORIGINAL pre-enrichment pointer, not the
		// intermediate clone, so callers fixing up r.models find their entry.
		replacements[orig]
	assert.Equal(t, providerModels["bge-m3"], got)
	assert.Nil(t, chat.Model.Metadata)
	assert.Equal(t, chat, providerModels["some-chat-model"])
}

// TestEnrichModels_RegistryDataWinsOverInference verifies that when the remote
// model list later supplies real metadata for a model the heuristic had
// classified, the registry data replaces the inferred modes.
func TestEnrichModels_RegistryDataWinsOverInference(t *testing.T) {
	registry := NewModelRegistry()

	local := &registryMockProvider{
		name: "provider-umma",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				// "gte-large" would be inferred as embedding; the model list
				// below deliberately declares it as chat to prove precedence.
				{ID: "gte-large", Object: "model", OwnedBy: "test"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(local, "umma", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{"gte-large":{"modes":["chat"]}},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)
	registry.EnrichModels()

	info := registry.GetModel("umma/gte-large")
	require.NotNil(t, info)
	require.NotNil(t, info.Model.Metadata)
	assert.Len(t, info.Model.Metadata.Modes, 1)
	assert.Equal(t, "chat", info.Model.Metadata.Modes[0])
}
