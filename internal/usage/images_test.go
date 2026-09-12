package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractFromImageResponse_NilResponse(t *testing.T) {
	entry := ExtractFromImageResponse(nil, "req", "dall-e-3", "openai")
	require.Nil(t, entry)
}

func TestExtractFromImageResponse_PerImagePricing(t *testing.T) {
	resp := &core.ImageGenerationResponse{Data: []core.ImageData{{URL: "https://a"}, {URL: "https://b"}}}
	pricing := &core.ModelPricing{PerImage: new(0.04)}

	entry := ExtractFromImageResponse(resp, "req-1", "dall-e-3", "openai", pricing)

	assert.Equal(t, endpointImageGenerations, entry.Endpoint)
	assert.Equal(t, "dall-e-3", entry.Model)
	assert.Equal(t, "openai", entry.Provider)
	assert.Equal(t, "req-1", entry.RequestID)
	assert.Equal(t, 0, entry.TotalTokens)
	assert.Equal(t, 2, entry.RawData[rawKeyImages])
	assert.NotNil(t, entry.OutputCost)
	assert.True(t, costsNearlyEqual(*entry.OutputCost, 0.08))
	assert.NotNil(t, entry.TotalCost)
	assert.True(t, costsNearlyEqual(*entry.TotalCost, 0.08))
	assert.Empty(t, entry.CostsCalculationCaveat)
}

func TestExtractFromImageResponse_TokenPricing(t *testing.T) {
	resp := &core.ImageGenerationResponse{
		Data: []core.ImageData{{B64JSON: "aGk="}},
		Usage: &core.ImageUsage{
			InputTokens:        100,
			OutputTokens:       1000,
			InputTokensDetails: &core.ImageTokenDetails{TextTokens: 100},
		},
	}
	pricing := &core.ModelPricing{InputPerMtok: new(5.0), OutputPerMtok: new(40.0)}

	entry := ExtractFromImageResponse(resp, "req-2", "gpt-image-1", "openai", pricing)

	assert.Equal(t, 100, entry.InputTokens)
	assert.Equal(t, 1000, entry.OutputTokens)
	assert.Equal(t, 1100, entry.TotalTokens)
	assert.Equal(t, 100, entry.RawData["prompt_text_tokens"])
	assert.Equal(t, 1, entry.RawData[rawKeyImages])

	// 100 * 5 / 1e6 + 1000 * 40 / 1e6 = 0.0005 + 0.04
	assert.NotNil(t, entry.TotalCost)
	assert.True(t, costsNearlyEqual(*entry.TotalCost, 0.0405))
	assert.Empty(t, entry.CostsCalculationCaveat)
}

func TestExtractFromImageResponse_NoPricing(t *testing.T) {
	entry := ExtractFromImageResponse(&core.ImageGenerationResponse{Data: []core.ImageData{{URL: "https://a"}}}, "req", "dall-e-3", "openai")
	assert.Nil(t, entry.TotalCost)
	assert.Equal(t, 1, entry.RawData[rawKeyImages])
}

func TestExtractFromImageEditResponse(t *testing.T) {
	entry := ExtractFromImageEditResponse(nil, "req", "gpt-image-1", "openai")
	require.Nil(t, entry)

	resp := &core.ImageGenerationResponse{
		Data: []core.ImageData{{B64JSON: "aGk="}},
		Usage: &core.ImageUsage{
			InputTokens: 50, OutputTokens: 1000, TotalTokens: 1050,
			InputTokensDetails: &core.ImageTokenDetails{TextTokens: 10, ImageTokens: 40},
		},
	}
	// gpt-image-1 prices generated image tokens at $40/Mtok and publishes
	// per_image as the flat equivalent of one high-quality image; the reported
	// tokens decide, so the edit costs 1000 * 40 / 1e6.
	pricing := &core.ModelPricing{OutputImagePerMtok: new(40.0), PerImage: new(0.167)}

	entry = ExtractFromImageEditResponse(resp, "req-2", "gpt-image-1", "openai", pricing)

	assert.Equal(t, endpointImageEdits, entry.Endpoint)
	assert.Equal(t, "gpt-image-1", entry.Model)
	assert.Equal(t, "openai", entry.Provider)
	assert.Equal(t, "req-2", entry.RequestID)
	assert.Equal(t, 50, entry.InputTokens)
	assert.Equal(t, 1000, entry.OutputTokens)
	assert.Equal(t, 1050, entry.TotalTokens)
	assert.Equal(t, 1, entry.RawData[rawKeyImages])
	assert.Equal(t, 40, entry.RawData["prompt_image_tokens"])
	assert.Equal(t, 10, entry.RawData["prompt_text_tokens"], "raw data = %v", entry.RawData)
	assert.NotNil(t, entry.OutputCost)
	assert.True(t, costsNearlyEqual(*entry.OutputCost, 0.04))
}

// TestImageBillingUnit pins the rule that decides how an image call is priced:
// a response that reports generated tokens is billed by those tokens at the
// image output rate, and only a response that reports none is billed per
// image. The catalog carries both rates for the same model (Gemini image
// models, gpt-image-1), where per_image is just the token price of one typical
// image, so applying both double-bills.
func TestImageBillingUnit(t *testing.T) {
	// gemini-2.5-flash-image: $0.30/Mtok in, $30/Mtok image out, and a
	// per_image of $0.039 — the price of one 1290-token image.
	gemini := &core.ModelPricing{
		InputPerMtok:       new(0.3),
		OutputPerMtok:      new(30.0),
		OutputImagePerMtok: new(30.0),
		PerImage:           new(0.039),
	}
	// gpt-image-1 has no text output rate at all: image tokens cost $40/Mtok,
	// and per_image is the flat price of a high-quality 1024x1024.
	gptImage1 := &core.ModelPricing{
		InputPerMtok:       new(5.0),
		OutputImagePerMtok: new(40.0),
		PerImage:           new(0.167),
	}
	// gpt-image-1-mini publishes only an image output rate.
	gptImage1Mini := &core.ModelPricing{InputPerMtok: new(2.0), OutputImagePerMtok: new(8.0)}
	// dall-e-3 reports no usage at all and is billed per image.
	dalle := &core.ModelPricing{PerImage: new(0.04)}

	tests := []struct {
		name       string
		pricing    *core.ModelPricing
		usage      *core.ImageUsage
		images     int
		wantCost   *float64
		wantCaveat string
	}{
		{
			name:     "gemini token-billed image is not also billed per image",
			pricing:  gemini,
			usage:    &core.ImageUsage{InputTokens: 5, OutputTokens: 1290, TotalTokens: 1295},
			images:   1,
			wantCost: new(5*0.3/1e6 + 1290*30/1e6),
		},
		{
			name:     "gpt-image-1 low quality is billed by its image tokens",
			pricing:  gptImage1,
			usage:    &core.ImageUsage{InputTokens: 9, OutputTokens: 272},
			images:   1,
			wantCost: new(9*5/1e6 + 272*40/1e6),
		},
		{
			name:     "gpt-image-1-mini prices its image output tokens",
			pricing:  gptImage1Mini,
			usage:    &core.ImageUsage{InputTokens: 9, OutputTokens: 272},
			images:   1,
			wantCost: new(9*2/1e6 + 272*8/1e6),
		},
		{
			name:     "a response without usage is billed per image",
			pricing:  dalle,
			images:   2,
			wantCost: new(0.08),
		},
		{
			name:       "reported tokens with no rate are flagged, not silently free",
			pricing:    &core.ModelPricing{InputPerMtok: new(2.0)},
			usage:      &core.ImageUsage{InputTokens: 9, OutputTokens: 272},
			images:     1,
			wantCost:   new(9 * 2 / 1e6),
			wantCaveat: caveatImageUnpricedOutput,
		},
		{
			name:       "no usage and no per-image rate stays flagged",
			pricing:    &core.ModelPricing{InputPerMtok: new(0.3), OutputPerMtok: new(30.0)},
			images:     1,
			wantCost:   new(0.0), // zero-token math, which is exactly why it is flagged
			wantCaveat: caveatImageMissingUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &core.ImageGenerationResponse{Usage: tt.usage}
			for range tt.images {
				resp.Data = append(resp.Data, core.ImageData{B64JSON: "aGk="})
			}

			entry := ExtractFromImageResponse(resp, "req", "m", "openai", tt.pricing)

			if tt.wantCost == nil {
				assert.Nil(t, entry.TotalCost)
			} else {
				require.NotNil(t, entry.TotalCost)
				assert.True(t, costsNearlyEqual(*entry.TotalCost, *tt.wantCost), "total cost = %v, want %v", *entry.TotalCost, *tt.wantCost)
			}
			assert.Equal(t, tt.wantCaveat, entry.CostsCalculationCaveat)
		})
	}
}

// TestImageCostCaveat_TimeWindowRate guards the caveat against disagreeing with
// the cost: when a time window supplies the output rate, the row is priced and
// must not also be flagged as unpriceable.
func TestImageCostCaveat_TimeWindowRate(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok: new(5.0),
		TimeWindows: []core.ModelPricingTimeWindow{{
			Label:     "always",
			UTCRanges: []core.ModelPricingUTCRange{{Start: "00:00", End: "00:00"}},
			Pricing:   core.ModelPricingTimeWindowRates{OutputPerMtok: new(40.0)},
		}},
	}
	resp := &core.ImageGenerationResponse{
		Data:  []core.ImageData{{B64JSON: "aGk="}},
		Usage: &core.ImageUsage{InputTokens: 9, OutputTokens: 272},
	}

	entry := ExtractFromImageResponse(resp, "req", "m", "openai", pricing)

	assert.NotNil(t, entry.TotalCost)
	assert.True(t, costsNearlyEqual(*entry.TotalCost, 9*5/1e6+272*40/1e6))
	assert.Empty(t, entry.CostsCalculationCaveat)
}

func TestExtractFromImageResponse_NoUsageCaveat(t *testing.T) {
	// A token-priced model whose serving surface returns no usage block
	// (e.g. Gemini's OpenAI-compatible images endpoint) must say why the
	// row carries no cost.
	resp := &core.ImageGenerationResponse{Data: []core.ImageData{{B64JSON: "aGk="}}}
	pricing := &core.ModelPricing{InputPerMtok: new(0.3), OutputPerMtok: new(30.0)}

	entry := ExtractFromImageResponse(resp, "req", "gemini-2.5-flash-image", "gemini", pricing)
	require.NotEmpty(t, entry.CostsCalculationCaveat)

	// With a per_image price the cost is real, so no caveat.
	priced := ExtractFromImageResponse(resp, "req", "dall-e-3", "openai", &core.ModelPricing{PerImage: new(0.04)})
	require.Empty(t, priced.CostsCalculationCaveat)

	// An explicit zero per_image price means a deliberately free model — its
	// known $0 cost must not read as unavailable.
	free := ExtractFromImageResponse(resp, "req", "free-image-model", "openai", &core.ModelPricing{PerImage: new(0.0)})
	require.Empty(t, free.CostsCalculationCaveat)

	// A per_image price with nothing to count is no basis: an empty response
	// without usage stays flagged.
	empty := ExtractFromImageResponse(&core.ImageGenerationResponse{}, "req", "dall-e-3", "openai", &core.ModelPricing{PerImage: new(0.04)})
	require.NotEmpty(t, empty.CostsCalculationCaveat)

	// A per-request price does not depend on usage at all, so it costs the
	// row on its own and needs no caveat.
	perRequest := ExtractFromImageResponse(resp, "req", "flat-rate-model", "openai", &core.ModelPricing{PerRequest: new(0.02)})
	require.Empty(t, perRequest.CostsCalculationCaveat)
	require.NotNil(t, perRequest.TotalCost)

	// A usage-carrying response keeps its token costs and stays caveat-free.
	withUsage := ExtractFromImageResponse(&core.ImageGenerationResponse{
		Data:  []core.ImageData{{B64JSON: "aGk="}},
		Usage: &core.ImageUsage{InputTokens: 10, OutputTokens: 1000, TotalTokens: 1010},
	}, "req", "gemini-2.5-flash-image", "gemini", pricing)
	require.Empty(t, withUsage.CostsCalculationCaveat)
	require.NotNil(t, withUsage.TotalCost)
}
