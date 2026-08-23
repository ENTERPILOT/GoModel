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

func TestExtractFromImageEditResponse(t *testing.T) {
	if entry := ExtractFromImageEditResponse(nil, "req", "gpt-image-1", "openai"); entry != nil {
		t.Fatalf("expected nil entry for nil response, got %+v", entry)
	}

	resp := &core.ImageGenerationResponse{
		Data: []core.ImageData{{B64JSON: "aGk="}},
		Usage: &core.ImageUsage{
			InputTokens: 50, OutputTokens: 1000, TotalTokens: 1050,
			InputTokensDetails: &core.ImageTokenDetails{TextTokens: 10, ImageTokens: 40},
		},
	}
	pricing := &core.ModelPricing{PerImage: new(0.04)}

	entry := ExtractFromImageEditResponse(resp, "req-2", "gpt-image-1", "openai", pricing)

	if entry.Endpoint != endpointImageEdits {
		t.Errorf("endpoint = %q, want %q", entry.Endpoint, endpointImageEdits)
	}
	if entry.Model != "gpt-image-1" || entry.Provider != "openai" || entry.RequestID != "req-2" {
		t.Errorf("identity = %q/%q/%q", entry.Provider, entry.Model, entry.RequestID)
	}
	if entry.InputTokens != 50 || entry.OutputTokens != 1000 || entry.TotalTokens != 1050 {
		t.Errorf("tokens = %d/%d/%d", entry.InputTokens, entry.OutputTokens, entry.TotalTokens)
	}
	if entry.RawData[rawKeyImages] != 1 || entry.RawData["prompt_image_tokens"] != 40 || entry.RawData["prompt_text_tokens"] != 10 {
		t.Errorf("raw data = %v", entry.RawData)
	}
	if entry.OutputCost == nil || !costsNearlyEqual(*entry.OutputCost, 0.04) {
		t.Errorf("output cost = %v, want 0.04 per-image", entry.OutputCost)
	}
}
