package usage

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestExtractFromImageResponse_NilResponse(t *testing.T) {
	if entry := ExtractFromImageResponse(nil, "req", "dall-e-3", "openai"); entry != nil {
		t.Fatalf("expected nil entry, got %+v", entry)
	}
}

func TestExtractFromImageResponse_PerImagePricing(t *testing.T) {
	resp := &core.ImageGenerationResponse{Data: []core.ImageData{{URL: "https://a"}, {URL: "https://b"}}}
	pricing := &core.ModelPricing{PerImage: new(0.04)}

	entry := ExtractFromImageResponse(resp, "req-1", "dall-e-3", "openai", pricing)

	if entry.Endpoint != endpointImageGenerations {
		t.Errorf("endpoint = %q, want %q", entry.Endpoint, endpointImageGenerations)
	}
	if entry.Model != "dall-e-3" || entry.Provider != "openai" || entry.RequestID != "req-1" {
		t.Errorf("identity = %q/%q/%q", entry.Provider, entry.Model, entry.RequestID)
	}
	if entry.TotalTokens != 0 {
		t.Errorf("tokens = %d, want 0 for a per-image model", entry.TotalTokens)
	}
	if got := entry.RawData[rawKeyImages]; got != 2 {
		t.Errorf("images = %v, want 2", got)
	}
	if entry.OutputCost == nil || !costsNearlyEqual(*entry.OutputCost, 0.08) {
		t.Errorf("output cost = %v, want 0.08", entry.OutputCost)
	}
	if entry.TotalCost == nil || !costsNearlyEqual(*entry.TotalCost, 0.08) {
		t.Errorf("total cost = %v, want 0.08", entry.TotalCost)
	}
	if entry.CostsCalculationCaveat != "" {
		t.Errorf("caveat = %q, want none", entry.CostsCalculationCaveat)
	}
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

	if entry.InputTokens != 100 || entry.OutputTokens != 1000 || entry.TotalTokens != 1100 {
		t.Errorf("tokens = %d/%d/%d, want 100/1000/1100 (total derived)", entry.InputTokens, entry.OutputTokens, entry.TotalTokens)
	}
	if got := entry.RawData["prompt_text_tokens"]; got != 100 {
		t.Errorf("prompt_text_tokens = %v, want 100", got)
	}
	if got := entry.RawData[rawKeyImages]; got != 1 {
		t.Errorf("images = %v, want 1", got)
	}
	// 100 * 5 / 1e6 + 1000 * 40 / 1e6 = 0.0005 + 0.04
	if entry.TotalCost == nil || !costsNearlyEqual(*entry.TotalCost, 0.0405) {
		t.Errorf("total cost = %v, want 0.0405", entry.TotalCost)
	}
	if entry.CostsCalculationCaveat != "" {
		t.Errorf("caveat = %q, want none (token details are informational)", entry.CostsCalculationCaveat)
	}
}

func TestExtractFromImageResponse_NoPricing(t *testing.T) {
	entry := ExtractFromImageResponse(&core.ImageGenerationResponse{Data: []core.ImageData{{URL: "https://a"}}}, "req", "dall-e-3", "openai")
	if entry.TotalCost != nil {
		t.Errorf("total cost = %v, want nil without pricing", entry.TotalCost)
	}
	if got := entry.RawData[rawKeyImages]; got != 1 {
		t.Errorf("images = %v, want 1", got)
	}
}

// TestExtractFromImageResponse_ImageTokenRates verifies token-billed image
// models are priced with the catalog's image token rates rather than the text
// rates: every output token of an image generation is an image token, and
// prompt image tokens are a breakdown of input_tokens priced at their own rate.
func TestExtractFromImageResponse_ImageTokenRates(t *testing.T) {
	tests := []struct {
		name      string
		usage     *core.ImageUsage
		pricing   *core.ModelPricing
		wantInput float64
		wantOut   float64
	}{
		{
			// gpt-image-1-mini catalog shape: no text output rate at all, so the
			// image output rate is the only charge on the output side.
			name:      "output image rate without text output rate",
			usage:     &core.ImageUsage{InputTokens: 17, OutputTokens: 272, InputTokensDetails: &core.ImageTokenDetails{TextTokens: 17}},
			pricing:   &core.ModelPricing{InputPerMtok: new(2.0), InputImagePerMtok: new(2.5), OutputImagePerMtok: new(8.0)},
			wantInput: 17 * 2.0 / 1e6,
			wantOut:   272 * 8.0 / 1e6,
		},
		{
			// gpt-image-1.5 catalog shape: a text output rate exists too; the
			// image rate must replace it for image tokens, not stack on top.
			name:      "output image rate overrides text output rate",
			usage:     &core.ImageUsage{InputTokens: 10, OutputTokens: 1000},
			pricing:   &core.ModelPricing{InputPerMtok: new(5.0), OutputPerMtok: new(10.0), OutputImagePerMtok: new(32.0)},
			wantInput: 10 * 5.0 / 1e6,
			wantOut:   1000 * 32.0 / 1e6,
		},
		{
			// Prompt image tokens (image edits / references) are part of
			// input_tokens: base rate for all, plus the image premium for those.
			name:      "prompt image tokens priced at image input rate",
			usage:     &core.ImageUsage{InputTokens: 100, OutputTokens: 0, InputTokensDetails: &core.ImageTokenDetails{TextTokens: 40, ImageTokens: 60}},
			pricing:   &core.ModelPricing{InputPerMtok: new(2.0), InputImagePerMtok: new(2.5)},
			wantInput: (100*2.0 + 60*(2.5-2.0)) / 1e6,
			wantOut:   0,
		},
		{
			// No image rates configured: fall back to the plain token rates.
			name:      "falls back to text rates",
			usage:     &core.ImageUsage{InputTokens: 100, OutputTokens: 200, InputTokensDetails: &core.ImageTokenDetails{ImageTokens: 60}},
			pricing:   &core.ModelPricing{InputPerMtok: new(1.0), OutputPerMtok: new(4.0)},
			wantInput: 100 * 1.0 / 1e6,
			wantOut:   200 * 4.0 / 1e6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &core.ImageGenerationResponse{Data: []core.ImageData{{B64JSON: "aGk="}}, Usage: tt.usage}
			entry := ExtractFromImageResponse(resp, "req", "gpt-image-1-mini", "openai", tt.pricing)

			if entry.InputCost == nil || !costsNearlyEqual(*entry.InputCost, tt.wantInput) {
				t.Errorf("input cost = %v, want %v", entry.InputCost, tt.wantInput)
			}
			if tt.wantOut == 0 {
				if entry.OutputCost != nil && *entry.OutputCost != 0 {
					t.Errorf("output cost = %v, want none", *entry.OutputCost)
				}
			} else if entry.OutputCost == nil || !costsNearlyEqual(*entry.OutputCost, tt.wantOut) {
				t.Errorf("output cost = %v, want %v", entry.OutputCost, tt.wantOut)
			}
			if entry.TotalCost == nil || !costsNearlyEqual(*entry.TotalCost, tt.wantInput+tt.wantOut) {
				t.Errorf("total cost = %v, want %v", entry.TotalCost, tt.wantInput+tt.wantOut)
			}
			if entry.CostsCalculationCaveat != "" {
				t.Errorf("caveat = %q, want none", entry.CostsCalculationCaveat)
			}
		})
	}
}

// TestCalculateGranularCost_PromptImageTokensStayInformationalForChat guards
// the chat vision path: prompt_image_tokens without an image input rate must
// keep pricing at the base input rate with no caveat.
func TestCalculateGranularCost_PromptImageTokensStayInformationalForChat(t *testing.T) {
	pricing := &core.ModelPricing{InputPerMtok: new(2.0), OutputPerMtok: new(8.0)}
	result := CalculateGranularCost(1000, 10, map[string]any{"prompt_image_tokens": 800, "prompt_text_tokens": 200}, "openai", pricing)
	if result.InputCost == nil || !costsNearlyEqual(*result.InputCost, 1000*2.0/1e6) {
		t.Errorf("input cost = %v, want base rate only", result.InputCost)
	}
	if result.Caveat != "" {
		t.Errorf("caveat = %q, want none", result.Caveat)
	}
}
