package usage

import (
	"math"
	"testing"

	"gomodel/internal/core"
)

func ptr(f float64) *float64 { return &f }

func TestCalculateGranularCost_NilPricing(t *testing.T) {
	result := CalculateGranularCost(100, 50, nil, "openai", nil)
	if result.InputCost != nil || result.OutputCost != nil || result.TotalCost != nil {
		t.Fatal("expected nil costs for nil pricing")
	}
	if result.Caveat != "" {
		t.Fatalf("expected empty caveat, got %q", result.Caveat)
	}
}

func TestCalculateGranularCost_BaseOnly(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(3.0),
		OutputPerMtok: ptr(15.0),
	}
	result := CalculateGranularCost(1_000_000, 500_000, nil, "openai", pricing)

	assertCostNear(t, "InputCost", result.InputCost, 3.0)
	assertCostNear(t, "OutputCost", result.OutputCost, 7.5)
	assertCostNear(t, "TotalCost", result.TotalCost, 10.5)
	if result.Caveat != "" {
		t.Fatalf("expected empty caveat, got %q", result.Caveat)
	}
}

func TestCalculateGranularCost_OpenAI_CachedAndReasoning(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:           ptr(2.50),
		OutputPerMtok:          ptr(10.0),
		CachedInputPerMtok:     ptr(1.25),
		ReasoningOutputPerMtok: ptr(15.0),
	}
	rawData := map[string]any{
		"cached_tokens":    200_000,
		"reasoning_tokens": 100_000,
	}
	result := CalculateGranularCost(500_000, 300_000, rawData, "openai", pricing)

	// Input: 500k * 2.50/1M + 200k * (1.25-2.50)/1M = 1.25 - 0.25 = 1.00
	assertCostNear(t, "InputCost", result.InputCost, 1.00)
	// Output: 300k * 10.0/1M + 100k * (15.0-10.0)/1M = 3.0 + 0.5 = 3.5
	assertCostNear(t, "OutputCost", result.OutputCost, 3.5)
	assertCostNear(t, "TotalCost", result.TotalCost, 4.5)
	if result.Caveat != "" {
		t.Fatalf("expected empty caveat, got %q", result.Caveat)
	}
}

func TestCalculateGranularCost_OpenAI_AudioTokens(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       ptr(2.50),
		OutputPerMtok:      ptr(10.0),
		AudioInputPerMtok:  ptr(100.0),
		AudioOutputPerMtok: ptr(200.0),
	}
	rawData := map[string]any{
		"prompt_audio_tokens":     50_000,
		"completion_audio_tokens": 30_000,
	}
	result := CalculateGranularCost(100_000, 80_000, rawData, "openai", pricing)

	// Input: 100k * 2.50/1M + 50k * (100-2.50)/1M = 0.25 + 4.875 = 5.125
	assertCostNear(t, "InputCost", result.InputCost, 5.125)
	// Output: 80k * 10/1M + 30k * (200-10)/1M = 0.80 + 5.70 = 6.50
	assertCostNear(t, "OutputCost", result.OutputCost, 6.50)
}

func TestCalculateGranularCost_Anthropic_CacheTokens(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:       ptr(3.0),
		OutputPerMtok:      ptr(15.0),
		CachedInputPerMtok: ptr(0.30),
		CacheWritePerMtok:  ptr(3.75),
	}
	rawData := map[string]any{
		"cache_read_input_tokens":     int64(100_000),
		"cache_creation_input_tokens": 50_000,
	}
	result := CalculateGranularCost(200_000, 100_000, rawData, "anthropic", pricing)

	// Input: 200k * 3.0/1M + 100k * 0.30/1M + 50k * 3.75/1M = 0.60 + 0.03 + 0.1875 = 0.8175
	assertCostNear(t, "InputCost", result.InputCost, 0.8175)
	// Output: 100k * 15.0/1M = 1.5
	assertCostNear(t, "OutputCost", result.OutputCost, 1.5)
}

func TestCalculateGranularCost_Gemini_ThoughtTokens(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:           ptr(1.25),
		OutputPerMtok:          ptr(5.0),
		CachedInputPerMtok:     ptr(0.3125),
		ReasoningOutputPerMtok: ptr(10.0),
	}
	rawData := map[string]any{
		"cached_tokens":  50_000,
		"thought_tokens": int(75_000),
	}
	result := CalculateGranularCost(100_000, 200_000, rawData, "gemini", pricing)

	// Input: 100k * 1.25/1M + 50k * (0.3125-1.25)/1M = 0.125 - 0.046875 = 0.078125
	assertCostNear(t, "InputCost", result.InputCost, 0.078125)
	// Output: 200k * 5.0/1M + 75k * (10.0-5.0)/1M = 1.0 + 0.375 = 1.375
	assertCostNear(t, "OutputCost", result.OutputCost, 1.375)
}

func TestCalculateGranularCost_XAI_ImageTokens(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(2.0),
		OutputPerMtok: ptr(10.0),
		InputPerImage: ptr(0.05), // $0.05 per image
	}
	rawData := map[string]any{
		"image_tokens": 3,
	}
	result := CalculateGranularCost(100_000, 50_000, rawData, "xai", pricing)

	// Input: 100k * 2.0/1M + 3 * 0.05 = 0.20 + 0.15 = 0.35
	assertCostNear(t, "InputCost", result.InputCost, 0.35)
}

func TestCalculateGranularCost_NilPricingFieldNoCaveat(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(2.50),
		OutputPerMtok: ptr(10.0),
		// CachedInputPerMtok is nil — base rate already covers cached tokens
	}
	rawData := map[string]any{
		"cached_tokens": 100_000,
	}
	result := CalculateGranularCost(500_000, 300_000, rawData, "openai", pricing)

	if result.Caveat != "" {
		t.Fatalf("expected no caveat when pricing field is nil (base rate covers it), got %q", result.Caveat)
	}
	// Base costs should still be calculated correctly without the adjustment
	assertCostNear(t, "InputCost", result.InputCost, 1.25)  // 500k * 2.50/1M
	assertCostNear(t, "OutputCost", result.OutputCost, 3.0) // 300k * 10.0/1M
}

func TestCalculateGranularCost_UnmappedTokenField(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(2.50),
		OutputPerMtok: ptr(10.0),
	}
	rawData := map[string]any{
		"some_new_tokens": 100,
	}
	result := CalculateGranularCost(100_000, 50_000, rawData, "openai", pricing)

	if result.Caveat == "" {
		t.Fatal("expected caveat for unmapped token field")
	}
	if result.Caveat != "unmapped token field: some_new_tokens" {
		t.Fatalf("unexpected caveat: %q", result.Caveat)
	}
}

func TestCalculateGranularCost_PerRequestFee(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(0.0),
		OutputPerMtok: ptr(0.0),
		PerRequest:    ptr(0.01),
	}
	result := CalculateGranularCost(100, 50, nil, "openai", pricing)

	assertCostNear(t, "OutputCost", result.OutputCost, 0.01)
	assertCostNear(t, "TotalCost", result.TotalCost, 0.01)
}

func TestCalculateGranularCost_UnknownProvider(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(1.0),
		OutputPerMtok: ptr(2.0),
	}
	rawData := map[string]any{
		"custom_tokens": 100,
	}
	result := CalculateGranularCost(1_000_000, 500_000, rawData, "unknown_provider", pricing)

	// Base costs should still work
	assertCostNear(t, "InputCost", result.InputCost, 1.0)
	assertCostNear(t, "OutputCost", result.OutputCost, 1.0)
	// Unmapped token field should produce caveat
	if result.Caveat != "unmapped token field: custom_tokens" {
		t.Fatalf("unexpected caveat: %q", result.Caveat)
	}
}

func TestCalculateGranularCost_ZeroTokenRawData(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(2.50),
		OutputPerMtok: ptr(10.0),
	}
	rawData := map[string]any{
		"cached_tokens": 0,
	}
	// Zero-value token fields should not produce caveats
	result := CalculateGranularCost(100_000, 50_000, rawData, "openai", pricing)
	if result.Caveat != "" {
		t.Fatalf("expected no caveat for zero token count, got %q", result.Caveat)
	}
}

func TestCalculateGranularCost_NonTokenField(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(2.50),
		OutputPerMtok: ptr(10.0),
	}
	rawData := map[string]any{
		"some_flag": true,
	}
	// Non-token fields should not produce caveats
	result := CalculateGranularCost(100_000, 50_000, rawData, "openai", pricing)
	if result.Caveat != "" {
		t.Fatalf("expected no caveat for non-token field, got %q", result.Caveat)
	}
}

func TestExtractInt(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]any
		key      string
		expected int
	}{
		{"float64", map[string]any{"k": float64(42)}, "k", 42},
		{"int", map[string]any{"k": 42}, "k", 42},
		{"int64", map[string]any{"k": int64(42)}, "k", 42},
		{"string", map[string]any{"k": "42"}, "k", 0},
		{"missing", map[string]any{}, "k", 0},
		{"nil map", nil, "k", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInt(tt.data, tt.key)
			if got != tt.expected {
				t.Fatalf("extractInt(%v, %q) = %d, want %d", tt.data, tt.key, got, tt.expected)
			}
		})
	}
}

func TestIsTokenField(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"cached_tokens", true},
		{"reasoning_tokens", true},
		{"prompt_token_count", true},
		{"some_flag", false},
		{"model", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := isTokenField(tt.key); got != tt.expected {
				t.Fatalf("isTokenField(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}

func TestCalculateGranularCost_XAI_PrefixedKeys(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:           ptr(2.0),
		OutputPerMtok:          ptr(10.0),
		CachedInputPerMtok:     ptr(0.50),
		ReasoningOutputPerMtok: ptr(15.0),
	}
	rawData := map[string]any{
		"prompt_cached_tokens":        200_000,
		"completion_reasoning_tokens": 100_000,
	}
	result := CalculateGranularCost(500_000, 300_000, rawData, "xai", pricing)

	// Input: 500k * 2.0/1M + 200k * (0.50-2.0)/1M = 1.0 - 0.30 = 0.70
	assertCostNear(t, "InputCost", result.InputCost, 0.70)
	// xAI reports reasoning tokens separately from completion_tokens, so they are charged in addition.
	// Output: 300k * 10.0/1M + 100k * 15.0/1M = 3.0 + 1.5 = 4.5
	assertCostNear(t, "OutputCost", result.OutputCost, 4.5)
	if result.Caveat != "" {
		t.Fatalf("expected no caveat for xAI prefixed keys, got %q", result.Caveat)
	}
}

func TestCalculateGranularCost_XAI_ReasoningTokensAreAdditionalOutput(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:           ptr(0.3),
		OutputPerMtok:          ptr(0.5),
		CachedInputPerMtok:     ptr(0.075),
		ReasoningOutputPerMtok: ptr(1.5),
	}
	rawData := map[string]any{
		"prompt_cached_tokens":        4,
		"completion_reasoning_tokens": 270,
	}
	result := CalculateGranularCost(12, 1, rawData, "xai", pricing)

	// Mirrors the live xAI chat shape: reasoning tokens are separate from completion_tokens.
	// Input: 12 * 0.3/1M + 4 * (0.075-0.3)/1M = 0.0000036 - 0.0000009 = 0.0000027
	assertCostNear(t, "InputCost", result.InputCost, 0.0000027)
	// Output: 1 * 0.5/1M + 270 * 1.5/1M = 0.0000005 + 0.000405 = 0.0004055
	assertCostNear(t, "OutputCost", result.OutputCost, 0.0004055)
	assertCostNear(t, "TotalCost", result.TotalCost, 0.0004082)
}

func TestCalculateGranularCost_InformationalFieldsNoCaveat(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(2.50),
		OutputPerMtok: ptr(10.0),
	}
	rawData := map[string]any{
		"prompt_text_tokens":                    80_000,
		"prompt_image_tokens":                   20_000,
		"completion_accepted_prediction_tokens": 5_000,
		"completion_rejected_prediction_tokens": 1_000,
	}
	result := CalculateGranularCost(100_000, 50_000, rawData, "openai", pricing)

	if result.Caveat != "" {
		t.Fatalf("expected no caveat for informational fields, got %q", result.Caveat)
	}
	assertCostNear(t, "InputCost", result.InputCost, 0.25)   // 100k * 2.50/1M
	assertCostNear(t, "OutputCost", result.OutputCost, 0.50) // 50k * 10.0/1M
}

func TestCalculateGranularCost_ReasoningModelNoCaveat(t *testing.T) {
	// Simulates the exact RawData produced by buildRawUsageFromDetails for o3-mini / grok-3-mini
	pricing := &core.ModelPricing{
		InputPerMtok:  ptr(1.10),
		OutputPerMtok: ptr(4.40),
		// No CachedInputPerMtok or ReasoningOutputPerMtok — base rate covers all
	}
	rawData := map[string]any{
		"prompt_cached_tokens":        0,
		"prompt_text_tokens":          500,
		"completion_reasoning_tokens": 1200,
	}
	result := CalculateGranularCost(500, 2000, rawData, "openai", pricing)

	if result.Caveat != "" {
		t.Fatalf("expected no caveat for reasoning model, got %q", result.Caveat)
	}
	// Input: 500 * 1.10/1M = 0.00055
	assertCostNear(t, "InputCost", result.InputCost, 0.00055)
	// Output: 2000 * 4.40/1M = 0.0088
	assertCostNear(t, "OutputCost", result.OutputCost, 0.0088)
}

func TestCalculateGranularCost_InputOnlyPricing(t *testing.T) {
	pricing := &core.ModelPricing{
		InputPerMtok: ptr(3.0),
		// OutputPerMtok is nil — no output pricing
	}
	result := CalculateGranularCost(1_000_000, 500_000, nil, "openai", pricing)

	assertCostNear(t, "InputCost", result.InputCost, 3.0)
	if result.OutputCost != nil {
		t.Fatalf("expected nil OutputCost, got %f", *result.OutputCost)
	}
	// TotalCost should still be set (input-only)
	assertCostNear(t, "TotalCost", result.TotalCost, 3.0)
}

func TestCalculateGranularCost_OutputOnlyPricing(t *testing.T) {
	pricing := &core.ModelPricing{
		// InputPerMtok is nil — no input pricing
		OutputPerMtok: ptr(15.0),
	}
	result := CalculateGranularCost(1_000_000, 500_000, nil, "openai", pricing)

	if result.InputCost != nil {
		t.Fatalf("expected nil InputCost, got %f", *result.InputCost)
	}
	assertCostNear(t, "OutputCost", result.OutputCost, 7.5)
	// TotalCost should still be set (output-only)
	assertCostNear(t, "TotalCost", result.TotalCost, 7.5)
}

func assertCostNear(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil, want %f", name, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s = %f, want %f", name, *got, want)
	}
}
