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
	// Zero-token math and an unpriced token count both yield $0 quietly —
	// indistinguishable from a free call. Flag whichever gap applies, so the row
	// reads as "we could not price this" rather than as a cheap call.
	if entry.CostsCalculationCaveat == "" {
		entry.CostsCalculationCaveat = imageCostCaveat(
			effectiveEndpointPricing(endpoint, entry.Timestamp, pricing...), entry.OutputTokens, len(resp.Data))
	}

	return entry
}

// isImageEndpoint reports whether an endpoint prices image output.
func isImageEndpoint(endpoint string) bool {
	return endpoint == endpointImageGenerations || endpoint == endpointImageEdits
}

// pricingForImageEndpoint resolves the output rate that applies to an image
// generation or edit: everything the model returns there is image output, which
// providers bill at output_image_per_mtok rather than the text output rate
// (gpt-image-1 has no text output rate at all, and Gemini 3 Pro Image charges
// $120/Mtok for image output against $12/Mtok for text).
func pricingForImageEndpoint(pricing *core.ModelPricing) *core.ModelPricing {
	if pricing == nil || pricing.OutputImagePerMtok == nil {
		return pricing
	}
	effective := *pricing
	effective.OutputPerMtok = pricing.OutputImagePerMtok
	// Volume tiers publish text rates; image output is billed at the image rate
	// whatever tier the input lands in.
	if len(pricing.Tiers) > 0 {
		effective.Tiers = make([]core.ModelPricingTier, len(pricing.Tiers))
		copy(effective.Tiers, pricing.Tiers)
		for i := range effective.Tiers {
			effective.Tiers[i].OutputPerMtok = nil
		}
	}
	return &effective
}
