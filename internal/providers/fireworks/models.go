package fireworks

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// modelsResponse mirrors Fireworks' /models payload. It restates the
// OpenAI-compatible fields core.Model already carries because Fireworks'
// entries also say what kind of model it is and what it supports, which the
// plain OpenAI shape drops.
type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
	// Kind is HF_BASE_MODEL, CUSTOM_MODEL or EMBEDDING_MODEL; embedding
	// models still report supports_chat, so kind decides the mode.
	Kind               string `json:"kind"`
	SupportsChat       bool   `json:"supports_chat"`
	SupportsImageInput bool   `json:"supports_image_input"`
	SupportsTools      bool   `json:"supports_tools"`
	ContextLength      int    `json:"context_length"`
}

// ListModels returns Fireworks' model listing, keeping the context window,
// the model kind and the feature flags each entry reports.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelsResponse
	if err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &upstream); err != nil {
		return nil, err
	}
	result := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(upstream.Data))}
	for _, model := range upstream.Data {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		result.Data = append(result.Data, model.toCore())
	}
	return result, nil
}

func (m modelInfo) toCore() core.Model {
	object := strings.TrimSpace(m.Object)
	if object == "" {
		object = "model"
	}
	return core.Model{
		ID:       strings.TrimSpace(m.ID),
		Object:   object,
		OwnedBy:  strings.TrimSpace(m.OwnedBy),
		Created:  m.Created,
		Metadata: m.metadata(),
	}
}

func (m modelInfo) metadata() *core.ModelMetadata {
	metadata := &core.ModelMetadata{}
	modes := m.modes()
	if len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(modes)
	}
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	if m.SupportsTools {
		metadata.Capabilities = providers.SetCapability(metadata.Capabilities, "function_calling", true)
	}
	if m.SupportsImageInput {
		metadata.Capabilities = providers.SetCapability(metadata.Capabilities, "vision", true)
	}
	if len(modes) == 0 && metadata.ContextWindow == nil && metadata.Capabilities == nil {
		return nil
	}
	return metadata
}

// modes derives the gateway modes from the model kind: embedding models
// serve /embeddings (rerankers are listed under the same kind and told apart
// by name), and everything else that supports chat is a chat model.
func (m modelInfo) modes() []string {
	if strings.EqualFold(strings.TrimSpace(m.Kind), "EMBEDDING_MODEL") {
		if strings.Contains(strings.ToLower(m.ID), "rerank") {
			return []string{"rerank"}
		}
		return []string{"embedding"}
	}
	if m.SupportsChat {
		return []string{"chat"}
	}
	return nil
}
