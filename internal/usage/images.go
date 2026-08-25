package usage

import (
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	endpointImageGenerations = "/v1/images/generations"
	endpointImageEdits       = "/v1/images/edits"

	// rawKeyImages carries the number of images a generation request returned.
	// Per-image models (DALL·E) report no token usage, so this is the billable
	// unit cost.go prices via PerImage; token-billed models (gpt-image-1) keep
	// it as an informational count alongside their token usage.
	rawKeyImages = "images"
)

// ExtractFromImageResponse builds a usage entry for an image generation call.
// Token usage is copied when the provider reports it (gpt-image-1); the image
// count is always recorded in RawData so the interaction stays observable and
// per-image pricing can apply even when the provider reports no tokens (DALL·E).
// model is the resolved route model so the row groups and prices consistently
// with the pricing lookup.
func ExtractFromImageResponse(resp *core.ImageGenerationResponse, requestID, model, provider string, pricing ...*core.ModelPricing) *UsageEntry {
	return extractFromImageResponse(resp, requestID, model, provider, endpointImageGenerations, pricing...)
}

// ExtractFromImageEditResponse builds a usage entry for an image edit call.
// Edits return the same envelope as generation and are priced the same way;
// only the endpoint label differs.
func ExtractFromImageEditResponse(resp *core.ImageGenerationResponse, requestID, model, provider string, pricing ...*core.ModelPricing) *UsageEntry {
	return extractFromImageResponse(resp, requestID, model, provider, endpointImageEdits, pricing...)
}

func extractFromImageResponse(resp *core.ImageGenerationResponse, requestID, model, provider, endpoint string, pricing ...*core.ModelPricing) *UsageEntry {
	if resp == nil {
		return nil
	}

	entry := &UsageEntry{
		ID:        uuid.New().String(),
		RequestID: requestID,
		Timestamp: time.Now().UTC(),
		Model:     model,
		Provider:  provider,
		Endpoint:  endpoint,
	}

	raw := map[string]any{}
	if count := len(resp.Data); count > 0 {
		raw[rawKeyImages] = count
	}
	if u := resp.Usage; u != nil {
		entry.InputTokens = u.InputTokens
		entry.OutputTokens = u.OutputTokens
		entry.TotalTokens = u.TotalTokens
		if entry.TotalTokens == 0 {
			entry.TotalTokens = u.InputTokens + u.OutputTokens
		}
		if d := u.InputTokensDetails; d != nil {
			if d.TextTokens > 0 {
				raw["prompt_text_tokens"] = d.TextTokens
			}
			if d.ImageTokens > 0 {
				raw["prompt_image_tokens"] = d.ImageTokens
			}
		}
	}
	if len(raw) > 0 {
		entry.RawData = raw
	}

	applyUsageCosts(entry, provider, endpoint, pricing...)
	// A response without a usage block cannot be costed by token rates, and
	// zero-token math quietly yields $0 — indistinguishable from a free call.
	// A per_image price (any value, including an explicit zero for a free
	// model) is a real basis only when there are images to count, so flag the
	// row unless both hold.
	if resp.Usage == nil && !perImagePriced(len(resp.Data), endpoint, pricing...) && entry.CostsCalculationCaveat == "" {
		entry.CostsCalculationCaveat = caveatImageMissingUsage
	}

	return entry
}

func perImagePriced(imageCount int, endpoint string, pricing ...*core.ModelPricing) bool {
	if imageCount <= 0 || len(pricing) == 0 || pricing[0] == nil {
		return false
	}
	effective := pricingForEndpoint(pricing[0], endpoint)
	return effective != nil && effective.PerImage != nil
}
