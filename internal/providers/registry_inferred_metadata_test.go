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
