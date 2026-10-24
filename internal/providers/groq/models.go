package groq

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// modelsResponse mirrors Groq's /models payload. It restates the
// OpenAI-compatible fields core.Model already carries because Groq's entries
// also describe the model: its context window, output limit, modalities,
// feature list and per-token prices, which the plain OpenAI shape drops.
type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID                  string        `json:"id"`
	Object              string        `json:"object"`
	OwnedBy             string        `json:"owned_by"`
	Created             int64         `json:"created"`
	Name                string        `json:"name"`
	ContextWindow       int           `json:"context_window"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	InputModalities     []string      `json:"input_modalities"`
	OutputModalities    []string      `json:"output_modalities"`
	SupportedFeatures   []string      `json:"supported_features"`
	Pricing             *modelPricing `json:"pricing"`
}

// modelPricing holds Groq's USD rates as decimal strings. Text models are
// priced per token; speech models per character of input text.
type modelPricing struct {
	Prompt         string `json:"prompt"`
	Completion     string `json:"completion"`
	InputCacheRead string `json:"input_cache_read"`
}

// ListModels returns Groq's model listing, keeping the context window,
// output limit, modalities, features and prices each entry reports.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelsResponse
	if err := p.compat.Do(ctx, llmclient.Request{
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
	modes := modesFromOutputModalities(m.OutputModalities)
	metadata := &core.ModelMetadata{
		DisplayName:  strings.TrimSpace(m.Name),
		Capabilities: providers.CapabilitiesFromInputModalities(providers.CapabilitiesFromFeatures(nil, m.SupportedFeatures), m.InputModalities),
		Pricing:      m.pricing(modes),
	}
	if len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(modes)
	}
	if m.ContextWindow > 0 {
		metadata.ContextWindow = new(m.ContextWindow)
	}
	if m.MaxCompletionTokens > 0 {
		metadata.MaxOutputTokens = new(m.MaxCompletionTokens)
	}
	if metadata.DisplayName == "" && metadata.Capabilities == nil && metadata.Pricing == nil &&
		len(modes) == 0 && metadata.ContextWindow == nil && metadata.MaxOutputTokens == nil {
		return nil
	}
	return metadata
}

// modesFromOutputModalities maps what a model produces onto gateway modes.
func modesFromOutputModalities(modalities []string) []string {
	modes := make([]string, 0, len(modalities))
	for _, modality := range modalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "text":
			modes = append(modes, "chat")
		case "speech":
			modes = append(modes, "audio_speech")
		case "transcription":
			modes = append(modes, "audio_transcription")
		}
	}
	return modes
}

// pricing converts Groq's rates into gateway pricing. Text models quote USD
// per token, scaled here to per million tokens; speech models quote the
// prompt rate per character of input text, which maps onto the per-character
// price rather than a token price.
func (m modelInfo) pricing(modes []string) *core.ModelPricing {
	if m.Pricing == nil {
		return nil
	}
	speech := len(modes) == 1 && modes[0] == "audio_speech"
	pricing := &core.ModelPricing{Currency: "USD"}
	found := false
	if speech {
		if rate, ok := perCharacter(m.Pricing.Prompt); ok {
			pricing.PerCharacterInput = &rate
			found = true
		}
	} else {
		if rate, ok := providers.PerTokenRateToMtok(m.Pricing.Prompt); ok {
			pricing.InputPerMtok = &rate
			found = true
		}
		if rate, ok := providers.PerTokenRateToMtok(m.Pricing.Completion); ok {
			pricing.OutputPerMtok = &rate
			found = true
		}
		if rate, ok := providers.PerTokenRateToMtok(m.Pricing.InputCacheRead); ok {
			pricing.CachedInputPerMtok = &rate
			found = true
		}
	}
	if !found {
		return nil
	}
	return pricing
}

func perCharacter(rate string) (float64, bool) {
	perMtok, ok := providers.PerTokenRateToMtok(rate)
	if !ok {
		return 0, false
	}
	return perMtok / 1_000_000, true
}
