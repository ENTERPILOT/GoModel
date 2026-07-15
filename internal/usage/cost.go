package usage

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	CostSourceModelPricing      = "model_pricing"
	CostSourceOpenRouterCredits = "openrouter_credits"
	CostSourceXAITicks          = "xai_cost_in_usd_ticks"
)

// xAI reports usage.cost_in_usd_ticks as USD-denominated ticks, where 10^10
// ticks equals 1 USD.
const xaiUSDTicksPerUSD = 10_000_000_000

// CostResult holds the result of a granular cost calculation.
type CostResult struct {
	InputCost  *float64
	OutputCost *float64
	TotalCost  *float64
	Caveat     string
	Source     string
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

var openAICompatibleTokenCostMappings = []tokenCostMapping{
	{rawDataKey: "cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
	{rawDataKey: "prompt_cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
	// DeepSeek reports cache hits as a top-level usage.prompt_cache_hit_tokens
	// field instead of the nested prompt_tokens_details.cached_tokens, and the
	// hit count is already part of prompt_tokens (prompt_tokens = hit + miss).
	// Treat it as a cached-input alias; the miss count needs no rate (the base
	// input rate already covers it) and is listed in informationalFields.
	{rawDataKey: "prompt_cache_hit_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
	{rawDataKey: "reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
	{rawDataKey: "completion_reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
	{rawDataKey: "prompt_audio_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.AudioInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
	{rawDataKey: "completion_audio_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.AudioOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
}

// providerMappings defines the per-provider RawData key to pricing field
// mappings. Providers not listed here fall back to
// openAICompatibleTokenCostMappings (see tokenCostMappingsForProvider): every
// other registered provider type (xiaomi, deepseek, zai, minimax, bailian,
// oracle, azure, vllm, ollama, opencode_go, …) speaks the OpenAI usage schema,
// so its cached/reasoning/audio token breakdowns must be priced the same way.
// Only providers whose usage schema differs (anthropic, gemini) or that report
// extra token types (xai) need an explicit entry.
var providerMappings = map[string][]tokenCostMapping{
	"openai":     openAICompatibleTokenCostMappings,
	"openrouter": openAICompatibleTokenCostMappings,
	"anthropic": {
		{rawDataKey: "cache_read_input_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok},
		{rawDataKey: "cache_creation_input_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CacheWritePerMtok }, side: sideInput, unit: unitPerMtok},
	},
	"gemini": {
		{rawDataKey: "cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "prompt_cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "cached_content_token_count", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "thought_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "thoughts_token_count", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "completion_reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "prompt_audio_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.AudioInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "completion_audio_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.AudioOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
	},
	"groq": {
		{rawDataKey: "cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "prompt_cached_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.CachedInputPerMtok }, side: sideInput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
		{rawDataKey: "completion_reasoning_tokens", pricingField: func(p *core.ModelPricing) *float64 { return p.ReasoningOutputPerMtok }, side: sideOutput, unit: unitPerMtok, includedInBase: true},
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
	// DeepSeek's cache-miss count: the non-cached remainder of prompt_tokens,
	// already priced at the base input rate (see prompt_cache_hit_tokens above).
	"prompt_cache_miss_tokens":              {},
	"prompt_text_tokens":                    {},
	"completion_text_tokens":                {},
	"prompt_image_tokens":                   {},
	"tool_use_prompt_token_count":           {},
	"completion_accepted_prediction_tokens": {},
	"completion_rejected_prediction_tokens": {},
}

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
	pricing = pricingForTokenCount(pricing, inputTokens)

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
	for _, m := range tokenCostMappingsForProvider(providerType) {
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

	// Price non-token audio units the providers report outside the usage tokens:
	// per-character input for text-to-speech, per-second input audio duration for
	// transcription, and per-second output audio duration for text-to-speech.
	// All quantities live in RawData (see usage/audio.go).
	if inAudio, outAudio, ok := applyAudioUnitCosts(rawData, pricing); ok {
		if inAudio > 0 {
			inputCost += inAudio
			hasInput = true
		}
		if outAudio > 0 {
			outputCost += outAudio
			hasOutput = true
		}
		mappedKeys[rawKeyInputCharacters] = true
		mappedKeys[rawKeyAudioSeconds] = true
		mappedKeys[rawKeyAudioOutputSeconds] = true
	}

	// Flag speech priced by output audio duration whose codec the gateway could
	// not measure (opus/aac/flac), so the cost reads as visibly
	// partial instead of a silent zero. Transcription rows never set the output
	// format key, so per-second-output transcription models are unaffected.
	if pricing.PerSecondOutput != nil {
		if _, measured := extractFloat(rawData, rawKeyAudioOutputSeconds); !measured {
			if format, ok := rawData[rawKeyAudioOutputFormat].(string); ok && format != "" {
				caveats = append(caveats, "audio output duration unmeasured for format: "+format)
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
		result.Source = CostSourceModelPricing
	}

	// Sort caveats for deterministic output
	sort.Strings(caveats)
	result.Caveat = strings.Join(caveats, "; ")

	return result
}

// applyAudioUnitCosts prices the non-token audio units carried in RawData:
// PerCharacterInput against input characters (text-to-speech) and PerSecondInput
// against input audio seconds (transcription) on the input side, and
// PerSecondOutput against synthesized audio seconds (text-to-speech) on the
// output side. It returns the input and output costs and whether any rate
// applied. Output duration is present only when the gateway could measure the
// returned audio (wav/pcm/mp3); other compressed formats are flagged with a
// caveat by the caller instead.
func applyAudioUnitCosts(rawData map[string]any, pricing *core.ModelPricing) (inputCost, outputCost float64, applied bool) {
	if pricing.PerCharacterInput != nil {
		if chars := extractInt(rawData, rawKeyInputCharacters); chars > 0 {
			inputCost += float64(chars) * *pricing.PerCharacterInput
			applied = true
		}
	}
	if pricing.PerSecondInput != nil {
		if seconds, ok := extractFloat(rawData, rawKeyAudioSeconds); ok && seconds > 0 {
			inputCost += seconds * *pricing.PerSecondInput
			applied = true
		}
	}
	if pricing.PerSecondOutput != nil {
		if seconds, ok := extractFloat(rawData, rawKeyAudioOutputSeconds); ok && seconds > 0 {
			outputCost += seconds * *pricing.PerSecondOutput
			applied = true
		}
	}
	return inputCost, outputCost, applied
}

func pricingForTokenCount(pricing *core.ModelPricing, inputTokens int) *core.ModelPricing {
	if pricing == nil || inputTokens <= 0 || len(pricing.Tiers) == 0 {
		return pricing
	}

	tier, ok := selectPricingTier(pricing.Tiers, inputTokens)
	if !ok {
		return pricing
	}

	effective := *pricing
	if tier.InputPerMtok != nil {
		effective.InputPerMtok = tier.InputPerMtok
	}
	if tier.OutputPerMtok != nil {
		effective.OutputPerMtok = tier.OutputPerMtok
	}
	return &effective
}

func selectPricingTier(tiers []core.ModelPricingTier, inputTokens int) (core.ModelPricingTier, bool) {
	type tierWithLimit struct {
		tier  core.ModelPricingTier
		limit float64
	}

	limited := make([]tierWithLimit, 0, len(tiers))
	for _, tier := range tiers {
		limit, ok := tierLimitTokens(tier)
		if !ok || limit <= 0 {
			continue
		}
		limited = append(limited, tierWithLimit{tier: tier, limit: limit})
	}
	if len(limited) == 0 {
		return core.ModelPricingTier{}, false
	}

	sort.Slice(limited, func(i, j int) bool {
		return limited[i].limit < limited[j].limit
	})

	tokenCount := float64(inputTokens)
	for _, candidate := range limited {
		if tokenCount <= candidate.limit {
			return candidate.tier, true
		}
	}
	return limited[len(limited)-1].tier, true
}

func tierLimitTokens(tier core.ModelPricingTier) (float64, bool) {
	if tier.UpToTokens != nil {
		return *tier.UpToTokens, true
	}
	if tier.UpToMtok != nil {
		return *tier.UpToMtok * 1_000_000, true
	}
	return 0, false
}

// tokenCostMappingsForProvider returns the token-cost mappings for a provider
// type, defaulting to the OpenAI-compatible mappings for the many providers
// that speak the OpenAI usage schema but are not listed in providerMappings.
// Without this default their cached/reasoning token breakdowns would be billed
// at the full input/output rate (see issue #435: Xiaomi cached tokens
// over-charged ~4x for cache-heavy workloads).
func tokenCostMappingsForProvider(providerType string) []tokenCostMapping {
	if mappings, ok := providerMappings[providerType]; ok {
		return mappings
	}
	return openAICompatibleTokenCostMappings
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
	case int32:
		return int(n)
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

// CalculateUsageCost prefers provider-supplied exact costs when available and
// falls back to static model pricing otherwise.
func CalculateUsageCost(inputTokens, outputTokens int, rawData map[string]any, providerType string, pricing *core.ModelPricing) CostResult {
	if result, ok := openRouterCreditCost(rawData, providerType); ok {
		return result
	}
	if result, ok := xaiTicksCost(rawData, providerType); ok {
		return result
	}
	return CalculateGranularCost(inputTokens, outputTokens, rawData, providerType, pricing)
}

func openRouterCreditCost(rawData map[string]any, providerType string) (CostResult, bool) {
	if !isOpenRouterProvider(providerType) {
		return CostResult{}, false
	}
	total, ok := extractFloat(rawData, "cost")
	if !ok || !isFiniteCost(total) || total < 0 {
		return CostResult{}, false
	}

	// OpenRouter reports cost in credits; their credit system is USD-based, so
	// this is the right value for GoModel's existing USD cost fields.
	result := CostResult{
		TotalCost: &total,
		Source:    CostSourceOpenRouterCredits,
	}
	if inputCost, outputCost, ok := openRouterCreditCostSplit(rawData, total); ok {
		result.InputCost = &inputCost
		result.OutputCost = &outputCost
	}
	return result, true
}

func isOpenRouterProvider(providerType string) bool {
	return strings.EqualFold(strings.TrimSpace(providerType), "openrouter")
}

func xaiTicksCost(rawData map[string]any, providerType string) (CostResult, bool) {
	if !isXAIProvider(providerType) {
		return CostResult{}, false
	}
	ticks, ok := extractFloat(rawData, "cost_in_usd_ticks")
	if !ok || !isFiniteCost(ticks) || ticks < 0 {
		return CostResult{}, false
	}

	total := ticks / xaiUSDTicksPerUSD
	return CostResult{
		TotalCost: &total,
		Source:    CostSourceXAITicks,
	}, true
}

func isXAIProvider(providerType string) bool {
	return strings.EqualFold(strings.TrimSpace(providerType), "xai")
}

func openRouterCreditCostSplit(rawData map[string]any, total float64) (float64, float64, bool) {
	details, ok := nestedUsageMap(rawData["cost_details"])
	if !ok {
		return 0, 0, false
	}

	input, inputOK := firstNestedFloat(details,
		"upstream_inference_prompt_cost",
		"upstream_inference_input_cost",
	)
	output, outputOK := firstNestedFloat(details,
		"upstream_inference_completions_cost",
		"upstream_inference_completion_cost",
		"upstream_inference_output_cost",
	)
	if !inputOK || !outputOK || !isFiniteCost(input) || !isFiniteCost(output) || input < 0 || output < 0 {
		return 0, 0, false
	}

	if !costsNearlyEqual(input+output, total) {
		return 0, 0, false
	}
	return input, output, true
}

func isFiniteCost(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func firstNestedFloat(data map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := extractFloat(data, key); ok {
			return value, true
		}
	}
	return 0, false
}

func nestedUsageMap(value any) (map[string]any, bool) {
	if typed, ok := value.(map[string]any); ok {
		return typed, true
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	out := make(map[string]any, reflected.Len())
	iter := reflected.MapRange()
	for iter.Next() {
		out[iter.Key().String()] = iter.Value().Interface()
	}
	return out, true
}

func extractFloat(data map[string]any, key string) (float64, bool) {
	if len(data) == 0 {
		return 0, false
	}
	value, ok := data[key]
	if !ok {
		return 0, false
	}
	return numericFloat(value)
}

func numericFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		f, err := strconv.ParseFloat(typed.String(), 64)
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func costsNearlyEqual(a, b float64) bool {
	return math.Abs(a-b) <= max(1e-12, math.Abs(b)*1e-6)
}
