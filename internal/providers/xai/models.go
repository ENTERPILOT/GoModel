package xai

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// modelsResponse mirrors xAI's /models payload. It restates the
// OpenAI-compatible fields core.Model already carries because xAI's entries
// also report the context window, prices and reasoning support, which the
// plain OpenAI shape drops.
type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

// modelInfo is one xAI listing entry. Token prices are integers in
// 1/10,000th of a USD cent, so 12500 per token is $1.25 per million tokens;
// image_price uses the same unit per generated image (200000000 = $0.02).
type modelInfo struct {
	ID            string   `json:"id"`
	Object        string   `json:"object"`
	OwnedBy       string   `json:"owned_by"`
	Created       int64    `json:"created"`
	Aliases       []string `json:"aliases"`
	ContextLength int      `json:"context_length"`

	PromptTextTokenPrice                *int64 `json:"prompt_text_token_price"`
	CachedPromptTextTokenPrice          *int64 `json:"cached_prompt_text_token_price"`
	PromptImageTokenPrice               *int64 `json:"prompt_image_token_price"`
	CompletionTextTokenPrice            *int64 `json:"completion_text_token_price"`
	PromptTextTokenPriceLongContext     *int64 `json:"prompt_text_token_price_long_context"`
	CompletionTextTokenPriceLongContext *int64 `json:"completion_text_token_price_long_context"`
	LongContextThreshold                *int64 `json:"long_context_threshold"`
	ImagePrice                          *int64 `json:"image_price"`

	Capabilities struct {
		ReasoningEffort []string `json:"reasoning_effort"`
	} `json:"capabilities"`
}

// priceUnitsPerUSD is how many xAI price units make one US dollar.
const priceUnitsPerUSD = 10_000_000_000

// ListModels returns xAI's model listing, keeping the context window, prices
// (including the long-context tier) and reasoning support each entry reports.
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

// namedReasoning recognizes the "-reasoning" variants xAI lists without a
// capabilities block, while leaving their "-non-reasoning" siblings alone.
func (m modelInfo) namedReasoning() bool {
	id := strings.ToLower(m.ID)
	return strings.Contains(id, "reasoning") && !strings.Contains(id, "non-reasoning")
}

func (m modelInfo) isImageModel() bool {
	return m.ImagePrice != nil
}

func (m modelInfo) isVideoModel() bool {
	return !m.isImageModel() && m.CompletionTextTokenPrice == nil && strings.Contains(strings.ToLower(m.ID), "video")
}

func (m modelInfo) metadata() *core.ModelMetadata {
	metadata := &core.ModelMetadata{}
	switch {
	case m.isImageModel():
		metadata.Modes = []string{"image_generation"}
		metadata.Pricing = imagePricing(m.ImagePrice)
	case m.isVideoModel():
		metadata.Modes = []string{"video_generation"}
	default:
		metadata.Modes = []string{"chat", "responses"}
		metadata.Pricing = m.textPricing()
		if len(m.Capabilities.ReasoningEffort) > 0 || m.namedReasoning() {
			metadata.Capabilities = providers.SetCapability(metadata.Capabilities, "reasoning", true)
		}
		if m.PromptImageTokenPrice != nil && *m.PromptImageTokenPrice > 0 {
			metadata.Capabilities = providers.SetCapability(metadata.Capabilities, "vision", true)
		}
	}
	metadata.Categories = core.CategoriesForModes(metadata.Modes)
	if m.ContextLength > 0 {
		metadata.ContextWindow = new(m.ContextLength)
	}
	return metadata
}

func unitsToUSD(units *int64) *float64 {
	if units == nil || *units < 0 {
		return nil
	}
	usd := float64(*units) / priceUnitsPerUSD
	return &usd
}

func unitsToUSDPerMtok(units *int64) *float64 {
	usd := unitsToUSD(units)
	if usd == nil {
		return nil
	}
	perMtok := *usd * 1_000_000
	return &perMtok
}

func imagePricing(units *int64) *core.ModelPricing {
	perImage := unitsToUSD(units)
	if perImage == nil {
		return nil
	}
	return &core.ModelPricing{Currency: "USD", PerImage: perImage}
}

// textPricing maps the base token rates and, when the model charges more past
// a prompt-length threshold, a two-tier schedule: the base rates up to the
// threshold and the long-context rates up to the context window.
func (m modelInfo) textPricing() *core.ModelPricing {
	pricing := &core.ModelPricing{
		Currency:           "USD",
		InputPerMtok:       unitsToUSDPerMtok(m.PromptTextTokenPrice),
		OutputPerMtok:      unitsToUSDPerMtok(m.CompletionTextTokenPrice),
		CachedInputPerMtok: unitsToUSDPerMtok(m.CachedPromptTextTokenPrice),
	}
	if pricing.InputPerMtok == nil && pricing.OutputPerMtok == nil && pricing.CachedInputPerMtok == nil {
		return nil
	}
	longInput := unitsToUSDPerMtok(m.PromptTextTokenPriceLongContext)
	longOutput := unitsToUSDPerMtok(m.CompletionTextTokenPriceLongContext)
	if m.LongContextThreshold != nil && *m.LongContextThreshold > 0 && m.ContextLength > int(*m.LongContextThreshold) &&
		(longInput != nil || longOutput != nil) {
		threshold := float64(*m.LongContextThreshold)
		limit := float64(m.ContextLength)
		pricing.Tiers = []core.ModelPricingTier{
			{UpToTokens: &threshold, InputPerMtok: pricing.InputPerMtok, OutputPerMtok: pricing.OutputPerMtok},
			{UpToTokens: &limit, InputPerMtok: longInput, OutputPerMtok: longOutput},
		}
	}
	return pricing
}
