package deepinfra

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

type modelsResponse struct {
	Data []modelInfo `json:"data"`
}

type modelInfo struct {
	ID       string         `json:"id"`
	Object   string         `json:"object"`
	OwnedBy  string         `json:"owned_by"`
	Created  int64          `json:"created"`
	Metadata *modelMetadata `json:"metadata"`
}

type modelMetadata struct {
	Description   string        `json:"description"`
	ContextLength int           `json:"context_length"`
	MaxTokens     int           `json:"max_tokens"`
	Pricing       *modelPricing `json:"pricing"`
	Tags          []string      `json:"tags"`
}

// modelPricing holds DeepInfra's USD rates: tokens per million, TTS input per
// million characters, STT input per second of audio, and images per unit.
type modelPricing struct {
	InputTokens     *float64 `json:"input_tokens"`
	OutputTokens    *float64 `json:"output_tokens"`
	CacheReadTokens *float64 `json:"cache_read_tokens"`
	InputCharacters *float64 `json:"input_characters"`
	InputSeconds    *float64 `json:"input_seconds"`
	PerImageUnit    *float64 `json:"per_image_unit"`
}

// tagModes maps DeepInfra's model kind tags onto gateway modes. "video-gen"
// is deliberately absent: the gateway has no video surface on DeepInfra.
var tagModes = map[string][]string{
	"chat":      {"chat", "responses"},
	"embed":     {"embedding"},
	"image-gen": {"image_generation"},
	"tts":       {"audio_speech"},
	"stt":       {"audio_transcription"},
}

// tagCapabilities maps DeepInfra's feature tags onto catalog capability keys.
var tagCapabilities = map[string]string{
	"reasoning":        "reasoning",
	"reasoning_effort": "reasoning",
	"vision":           "vision",
	"vlm":              "vision",
	"prompt_cache":     "prompt_caching",
}

// ListModels returns DeepInfra's model catalog with the context, pricing, and
// kind tags its OpenAI-compatible listing carries. Models whose tags name
// only kinds the gateway cannot serve (video generation) are skipped so the
// catalog never advertises a model that can only fail.
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
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		metadata, servable := model.Metadata.toCore()
		if !servable {
			continue
		}
		object := strings.TrimSpace(model.Object)
		if object == "" {
			object = "model"
		}
		result.Data = append(result.Data, core.Model{
			ID:       id,
			Object:   object,
			OwnedBy:  strings.TrimSpace(model.OwnedBy),
			Created:  model.Created,
			Metadata: metadata,
		})
	}
	return result, nil
}

// toCore normalizes DeepInfra's listing metadata. It reports false when the
// tags name model kinds but none the gateway serves; a model without tags
// gives no signal and is kept.
func (m *modelMetadata) toCore() (*core.ModelMetadata, bool) {
	if m == nil {
		return nil, true
	}
	var modes []string
	var capabilities map[string]bool
	kindTagged := false
	for _, tag := range m.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "video-gen" {
			kindTagged = true
		}
		if tagged, ok := tagModes[tag]; ok {
			kindTagged = true
			modes = append(modes, tagged...)
		}
		if key, ok := tagCapabilities[tag]; ok {
			capabilities = providers.SetCapability(capabilities, key, true)
		}
	}
	if kindTagged && len(modes) == 0 {
		return nil, false
	}

	metadata := &core.ModelMetadata{
		Description:  strings.TrimSpace(m.Description),
		Capabilities: capabilities,
		Pricing:      m.Pricing.toCore(),
	}
	if len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(modes)
	}
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	if m.MaxTokens > 0 {
		metadata.MaxOutputTokens = new(m.MaxTokens)
	}
	return metadata, true
}

// toCore converts DeepInfra's rates to GoModel pricing metadata, scaling the
// per-million-character speech rate down to a per-character price.
func (p *modelPricing) toCore() *core.ModelPricing {
	if p == nil {
		return nil
	}
	pricing := &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       cloneFloat(p.InputTokens),
		OutputPerMtok:      cloneFloat(p.OutputTokens),
		CachedInputPerMtok: cloneFloat(p.CacheReadTokens),
		PerSecondInput:     cloneFloat(p.InputSeconds),
		PerImage:           cloneFloat(p.PerImageUnit),
	}
	if p.InputCharacters != nil {
		pricing.PerCharacterInput = new(*p.InputCharacters / 1_000_000)
	}
	if pricing.InputPerMtok == nil && pricing.OutputPerMtok == nil && pricing.CachedInputPerMtok == nil &&
		pricing.PerSecondInput == nil && pricing.PerImage == nil && pricing.PerCharacterInput == nil {
		return nil
	}
	return pricing
}

// cloneFloat copies optional prices so the normalized model does not retain upstream pointers.
func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
