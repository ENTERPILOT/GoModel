package chutes

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID                  string        `json:"id"`
	Object              string        `json:"object"`
	OwnedBy             string        `json:"owned_by"`
	Created             int64         `json:"created"`
	ContextLength       int           `json:"context_length"`
	MaxOutputLength     int           `json:"max_output_length"`
	InputModalities     []string      `json:"input_modalities"`
	SupportedFeatures   []string      `json:"supported_features"`
	ConfidentialCompute bool          `json:"confidential_compute"`
	Pricing             *modelPricing `json:"pricing"`
}

type modelPricing struct {
	Prompt         *float64 `json:"prompt"`
	Completion     *float64 `json:"completion"`
	InputCacheRead *float64 `json:"input_cache_read"`
}

// ListModels returns Chutes' live model catalog while retaining the context,
// feature, and per-million-token pricing data included in its response.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelsResponse
	if err := p.compat.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &upstream); err != nil {
		return nil, err
	}

	result := &core.ModelsResponse{Object: "list"}
	result.Data = make([]core.Model, 0, len(upstream.Data))
	for _, model := range upstream.Data {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		result.Data = append(result.Data, model.toCore())
	}
	return result, nil
}

// toCore normalizes a Chutes catalog entry into GoModel's provider-neutral model shape.
func (m modelInfo) toCore() core.Model {
	object := strings.TrimSpace(m.Object)
	if object == "" {
		object = "model"
	}

	modes := []string{"chat", "responses"}
	metadata := &core.ModelMetadata{
		Modes:        modes,
		Categories:   core.CategoriesForModes(modes),
		Capabilities: modelCapabilities(m),
		Pricing:      m.Pricing.toCore(),
	}
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	if m.MaxOutputLength > 0 {
		metadata.MaxOutputTokens = new(m.MaxOutputLength)
	}

	return core.Model{
		ID:       strings.TrimSpace(m.ID),
		Object:   object,
		OwnedBy:  strings.TrimSpace(m.OwnedBy),
		Created:  m.Created,
		Metadata: metadata,
	}
}

// modelCapabilities maps Chutes features and input modalities to GoModel capabilities.
func modelCapabilities(m modelInfo) map[string]bool {
	capabilities := make(map[string]bool, len(m.SupportedFeatures)+3)
	for _, feature := range m.SupportedFeatures {
		if normalized := strings.ToLower(strings.TrimSpace(feature)); normalized != "" {
			capabilities[normalized] = true
		}
	}
	for _, modality := range m.InputModalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image":
			capabilities["vision"] = true
		case "video":
			capabilities["video"] = true
		case "audio":
			capabilities["audio"] = true
		}
	}
	if m.ConfidentialCompute {
		capabilities["confidential_compute"] = true
	}
	if len(capabilities) == 0 {
		return nil
	}
	return capabilities
}

// toCore converts Chutes' per-million-token prices to GoModel pricing metadata.
func (p *modelPricing) toCore() *core.ModelPricing {
	if p == nil || (p.Prompt == nil && p.Completion == nil && p.InputCacheRead == nil) {
		return nil
	}
	return &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       cloneFloat(p.Prompt),
		OutputPerMtok:      cloneFloat(p.Completion),
		CachedInputPerMtok: cloneFloat(p.InputCacheRead),
	}
}

// cloneFloat copies optional prices so the normalized model does not retain upstream pointers.
func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
