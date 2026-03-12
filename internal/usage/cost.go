package usage

import (
	"fmt"
	"sort"
	"strings"

	"gomodel/internal/core"
)

// CostResult holds the result of a granular cost calculation.
type CostResult struct {
	InputCost  *float64
	OutputCost *float64
	TotalCost  *float64
	Caveat     string
}

// costSide indicates whether a token cost contributes to input or output.
type costSide int

const (
	sideInput costSide = iota
	sideOutput
)

// costUnit indicates how the pricing field is applied.
type costUnit int

const (
	unitPerMtok costUnit = iota // divide token count by 1M, multiply by rate
	unitPerItem                 // multiply count directly by rate
)

// tokenCostMapping maps a RawData key to a pricing field and cost side.
type tokenCostMapping struct {
	rawDataKey     string
	pricingField   func(p *core.ModelPricing) *float64
	side           costSide
	unit           costUnit
	includedInBase bool
}

// providerMappings defines the per-provider RawData key to pricing field mappings.
var providerMappings = map[string][]tokenCostMapping{
	"openai": {
		{rawDataKey: "cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "prompt_cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "completion_reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "prompt_audio_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.AudioInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "completion_audio_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.AudioOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
	},
	"anthropic": {
		{rawDataKey: "cache_read_input_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok},
		{rawDataKey: "cache_creation_input_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CacheWritePerMtok }, side: sideInput, unit: unitPerMtok},
	},
	"gemini": {
		{rawDataKey: "cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "thought_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
	},
	"xai": {
		{rawDataKey: "cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "prompt_cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		// xAI reports reasoning tokens separately from completion_tokens.
		{rawDataKey: "reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok},
		{rawDataKey: "completion_reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok},
		{rawDataKey: "image_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.InputPerImage }, side: sideInput, unit: unitPerItem},
	},
}

// informationalFields are token fields that are known breakdowns of the base
// input/output counts. They never need separate pricing and should not trigger
// "unmapped token field" caveats.
var informationalFields = map[string]struct{}{
	"prompt_text_tokens":                    {},
	"prompt_image_tokens":                   {},
	"completion_accepted_prediction_tokens": {},
	"completion_rejected_prediction_tokens": {},
}

// extendedFieldSet is derived from providerMappings and contains all RawData keys
// that providers may report. Used by stream_wrapper.go to extract extended fields
// from SSE usage data without maintaining a separate hard-coded list.
var extendedFieldSet = func() map[string]struct{} {
	set := make(map[string]struct{})
	for _, mappings := range providerMappings {
		for _, m := range mappings {
			set[m.rawDataKey] = struct{}{}
		}
	}
	return set
}()

// CalculateGranularCost computes input, output, and total costs from token counts,
// raw provider-specific data, and pricing information. It accounts for cached tokens,
// reasoning tokens, audio tokens, and other provider-specific token types.
//
// The caveat field in the result describes any unmapped token fields or missing pricing
// data that prevented full cost calculation.
func CalculateGranularCost(inputTokens, outputTokens int, rawData map[string]any, providerType string, pricing *core.ModelPricing) CostResult {
	if pricing == nil {
		return CostResult{}
	}

	var inputCost, outputCost float64
	var hasInput, hasOutput bool
	var caveats []string

	// Track which RawData keys are mapped
	mappedKeys := make(map[string]bool)

	// Base input cost
	if pricing.InputPerMtok != nil {
		inputCost += float64(inputTokens) * *pricing.InputPerMtok / 1_000_000
		hasInput = true
	}

	// Base output cost
	if pricing.OutputPerMtok != nil {
		outputCost += float64(outputTokens) * *pricing.OutputPerMtok / 1_000_000
		hasOutput = true
	}

	// Apply provider-specific mappings.
	// Track applied pricing field pointers to avoid double-counting when multiple
	// rawData keys map to the same pricing field (e.g. cached_tokens and prompt_cached_tokens
	// both map to CachedInputPerMtok).
	appliedFields := make(map[*float64]bool)
	if mappings, ok := providerMappings[providerType]; ok {
		for _, m := range mappings {
			count := extractInt(rawData, m.rawDataKey)
			if count == 0 {
				continue
			}
			mappedKeys[m.rawDataKey] = true

			rate := m.pricingField(pricing)
			if rate == nil {
				continue // Base rate covers this token type; no adjustment needed
			}

			if appliedFields[rate] {
				continue // Already applied via a different rawData key for the same pricing field
			}
			appliedFields[rate] = true

			effectiveRate := *rate
			if m.includedInBase && m.unit == unitPerMtok {
				if baseRate := baseRateForSide(pricing, m.side); baseRate != nil {
					effectiveRate -= *baseRate
				}
			}

			var cost float64
			switch m.unit {
			case unitPerMtok:
				cost = float64(count) * effectiveRate / 1_000_000
			case unitPerItem:
				cost = float64(count) * effectiveRate
			}

			switch m.side {
			case sideInput:
				inputCost += cost
				hasInput = true
			case sideOutput:
				outputCost += cost
				hasOutput = true
			}
		}
	}

	// Check for unmapped token fields in RawData
	for key := range rawData {
		if mappedKeys[key] {
			continue
		}
		if _, ok := informationalFields[key]; ok {
			continue // Known breakdown of base counts, not separately priced
		}
		if isTokenField(key) {
			count := extractInt(rawData, key)
			if count > 0 {
				caveats = append(caveats, fmt.Sprintf("unmapped token field: %s", key))
			}
		}
	}

	// Add per-request flat fee
	if pricing.PerRequest != nil {
		outputCost += *pricing.PerRequest
		hasOutput = true
	}

	result := CostResult{}

	if hasInput {
		result.InputCost = &inputCost
	}
	if hasOutput {
		result.OutputCost = &outputCost
	}
	if hasInput || hasOutput {
		total := inputCost + outputCost
		result.TotalCost = &total
	}

	// Sort caveats for deterministic output
	sort.Strings(caveats)
	result.Caveat = strings.Join(caveats, "; ")

	return result
}

func baseRateForSide(pricing *core.ModelPricing, side costSide) *float64 {
	if pricing == nil {
		return nil
	}
	switch side {
	case sideInput:
		return pricing.InputPerMtok
	case sideOutput:
		return pricing.OutputPerMtok
	default:
		return nil
	}
}

// extractInt extracts an integer value from a map, handling float64, int, and int64 types.
// Returns 0 if the key is not found or the value is not a numeric type.
func extractInt(data map[string]any, key string) int {
	v, ok := data[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// isTokenField returns true if the key looks like a token count field.
func isTokenField(key string) bool {
	return strings.HasSuffix(key, "_tokens") || strings.HasSuffix(key, "_count")
}
