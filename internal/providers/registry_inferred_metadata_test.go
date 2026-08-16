package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
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

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	for _, id := range []string{"nomic-embed-text-v1.5.Q8_0.gguf", "bge-m3"} {
		info := registry.GetModel("lagash/" + id)
		if info == nil || info.Model.Metadata == nil {
			t.Fatalf("expected %s to have inferred metadata", id)
		}
		meta := info.Model.Metadata
		if len(meta.Modes) != 1 || meta.Modes[0] != "embedding" {
			t.Errorf("%s Modes = %v, want [embedding]", id, meta.Modes)
		}
		if len(meta.Categories) != 1 || meta.Categories[0] != core.CategoryEmbedding {
			t.Errorf("%s Categories = %v, want [embedding]", id, meta.Categories)
		}
	}

	if info := registry.GetModel("lagash/llama-3.1-8b-instruct"); info == nil {
		t.Fatal("expected llama-3.1-8b-instruct to be registered")
	} else if info.Model.Metadata != nil {
		t.Errorf("llama-3.1-8b-instruct metadata = %+v, want nil (no inference)", info.Model.Metadata)
	}

	embeddings := registry.ListModelsWithProviderByCategory(core.CategoryEmbedding)
	found := map[string]bool{}
	for _, m := range embeddings {
		found[m.Model.ID] = true
	}
	if !found["nomic-embed-text-v1.5.Q8_0.gguf"] || !found["bge-m3"] {
		t.Errorf("embedding category listing = %v, want both local embedding models", found)
	}
	if found["llama-3.1-8b-instruct"] {
		t.Error("chat model must not appear in the embedding category")
	}
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
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}

	// Original pointers must be untouched; new entries carry the metadata.
	if fresh.Model.Metadata != nil || prior.Model.Metadata != nil {
		t.Error("published ModelInfo values were mutated in place")
	}
	for _, id := range []string{"nomic-embed-text", "bge-m3"} {
		next := providerModels[id]
		if next.Model.Metadata == nil || len(next.Model.Metadata.Modes) != 1 || next.Model.Metadata.Modes[0] != "embedding" {
			t.Errorf("%s replacement metadata = %+v, want embedding modes", id, next.Model.Metadata)
		}
	}
	if got := replacements[fresh]; got != providerModels["nomic-embed-text"] {
		t.Error("fresh entry not recorded in replacements")
	}
	// The chain must point from the ORIGINAL pre-enrichment pointer, not the
	// intermediate clone, so callers fixing up r.models find their entry.
	if got := replacements[orig]; got != providerModels["bge-m3"] {
		t.Error("replacement chain broken: orig does not map to the final entry")
	}
	if chat.Model.Metadata != nil || providerModels["some-chat-model"] != chat {
		t.Error("non-inferable model must be left untouched")
	}
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

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{"gte-large":{"modes":["chat"]}},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	registry.SetModelList(list, raw)
	registry.EnrichModels()

	info := registry.GetModel("umma/gte-large")
	if info == nil || info.Model.Metadata == nil {
		t.Fatal("expected gte-large to have metadata")
	}
	if len(info.Model.Metadata.Modes) != 1 || info.Model.Metadata.Modes[0] != "chat" {
		t.Errorf("Modes = %v, want [chat] from model list", info.Model.Metadata.Modes)
	}
}
