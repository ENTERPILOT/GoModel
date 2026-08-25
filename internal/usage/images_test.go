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

func TestExtractFromImageResponse_NoUsageCaveat(t *testing.T) {
	// A token-priced model whose serving surface returns no usage block
	// (e.g. Gemini's OpenAI-compatible images endpoint) must say why the
	// row carries no cost.
	resp := &core.ImageGenerationResponse{Data: []core.ImageData{{B64JSON: "aGk="}}}
	pricing := &core.ModelPricing{InputPerMtok: new(0.3), OutputPerMtok: new(30.0)}

	entry := ExtractFromImageResponse(resp, "req", "gemini-2.5-flash-image", "gemini", pricing)
	if entry.CostsCalculationCaveat == "" {
		t.Fatal("expected a caveat for a usage-less response without per-image pricing")
	}

	// With a per_image price the cost is real, so no caveat.
	priced := ExtractFromImageResponse(resp, "req", "dall-e-3", "openai", &core.ModelPricing{PerImage: new(0.04)})
	if priced.CostsCalculationCaveat != "" {
		t.Fatalf("caveat = %q, want none when per-image cost was calculated", priced.CostsCalculationCaveat)
	}

	// An explicit zero per_image price means a deliberately free model — its
	// known $0 cost must not read as unavailable.
	free := ExtractFromImageResponse(resp, "req", "free-image-model", "openai", &core.ModelPricing{PerImage: new(0.0)})
	if free.CostsCalculationCaveat != "" {
		t.Fatalf("caveat = %q, want none for an explicitly free per-image model", free.CostsCalculationCaveat)
	}

	// A per_image price with nothing to count is no basis: an empty response
	// without usage stays flagged.
	empty := ExtractFromImageResponse(&core.ImageGenerationResponse{}, "req", "dall-e-3", "openai", &core.ModelPricing{PerImage: new(0.04)})
	if empty.CostsCalculationCaveat == "" {
		t.Fatal("expected a caveat when there are no images and no usage to price")
	}

	// A usage-carrying response keeps its token costs and stays caveat-free.
	withUsage := ExtractFromImageResponse(&core.ImageGenerationResponse{
		Data:  []core.ImageData{{B64JSON: "aGk="}},
		Usage: &core.ImageUsage{InputTokens: 10, OutputTokens: 1000, TotalTokens: 1010},
	}, "req", "gemini-2.5-flash-image", "gemini", pricing)
	if withUsage.CostsCalculationCaveat != "" || withUsage.TotalCost == nil {
		t.Fatalf("entry = caveat %q cost %v, want costed and caveat-free", withUsage.CostsCalculationCaveat, withUsage.TotalCost)
	}
}
