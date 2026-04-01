package usage

import (
	"encoding/json"
	"maps"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

// buildRawUsageFromDetails merges typed token detail fields into a RawUsage map.
// Keys use the same "prompt_" / "completion_" prefix convention as stream_wrapper.go,
// which cost.go providerMappings already consume.
func buildRawUsageFromDetails(ptd *core.PromptTokensDetails, ctd *core.CompletionTokensDetails) map[string]any {
	raw := make(map[string]any)
	if ptd != nil {
		if ptd.CachedTokens > 0 {
			raw["prompt_cached_tokens"] = ptd.CachedTokens
		}
		if ptd.AudioTokens > 0 {
			raw["prompt_audio_tokens"] = ptd.AudioTokens
		}
		if ptd.TextTokens > 0 {
			raw["prompt_text_tokens"] = ptd.TextTokens
		}
		if ptd.ImageTokens > 0 {
			raw["prompt_image_tokens"] = ptd.ImageTokens
		}
	}
	if ctd != nil {
		if ctd.ReasoningTokens > 0 {
			raw["completion_reasoning_tokens"] = ctd.ReasoningTokens
		}
		if ctd.AudioTokens > 0 {
			raw["completion_audio_tokens"] = ctd.AudioTokens
		}
		if ctd.AcceptedPredictionTokens > 0 {
			raw["completion_accepted_prediction_tokens"] = ctd.AcceptedPredictionTokens
		}
		if ctd.RejectedPredictionTokens > 0 {
			raw["completion_rejected_prediction_tokens"] = ctd.RejectedPredictionTokens
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// ExtractFromChatResponse extracts usage data from a ChatResponse.
// It normalizes the usage data into a UsageEntry and preserves raw extended data.
// If pricing is provided, granular cost fields are calculated.
// For `/v1/batches` endpoints (exact or subpath), batch pricing overrides
// (BatchInputPerMtok/BatchOutputPerMtok) may replace standard input/output rates.
func ExtractFromChatResponse(resp *core.ChatResponse, requestID, provider, endpoint string, pricing ...*core.ModelPricing) *UsageEntry {
	if resp == nil {
		return nil
	}

	entry := &UsageEntry{
		ID:           uuid.New().String(),
		RequestID:    requestID,
		ProviderID:   resp.ID,
		Timestamp:    time.Now().UTC(),
		Model:        resp.Model,
		Provider:     provider,
		Endpoint:     endpoint,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}

	// Preserve raw extended usage data if available (defensive copy to avoid races)
	if len(resp.Usage.RawUsage) > 0 {
		entry.RawData = cloneRawData(resp.Usage.RawUsage)
	}

	// Merge typed detail fields into RawData (non-streaming path).
	// Only fill from details when RawUsage wasn't already set by the provider.
	if entry.RawData == nil {
		entry.RawData = buildRawUsageFromDetails(resp.Usage.PromptTokensDetails, resp.Usage.CompletionTokensDetails)
	}

	// Calculate granular costs if pricing is provided
	if len(pricing) > 0 && pricing[0] != nil {
		effectivePricing := pricingForEndpoint(pricing[0], endpoint)
		costResult := CalculateGranularCost(entry.InputTokens, entry.OutputTokens, entry.RawData, provider, effectivePricing)
		entry.InputCost = costResult.InputCost
		entry.OutputCost = costResult.OutputCost
		entry.TotalCost = costResult.TotalCost
		entry.CostsCalculationCaveat = costResult.Caveat
	}

	return entry
}

// cloneRawData creates a shallow copy of the raw data map to prevent races
// when the original map might be mutated after the entry is enqueued.
func cloneRawData(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}

// ExtractFromResponsesResponse extracts usage data from a ResponsesResponse.
// It normalizes the usage data into a UsageEntry and preserves raw extended data.
// If pricing is provided, cost fields are calculated.
// For `/v1/batches` endpoints (exact or subpath), batch pricing overrides
// (BatchInputPerMtok/BatchOutputPerMtok) may replace standard input/output rates.
func ExtractFromResponsesResponse(resp *core.ResponsesResponse, requestID, provider, endpoint string, pricing ...*core.ModelPricing) *UsageEntry {
	if resp == nil {
		return nil
	}

	entry := &UsageEntry{
		ID:         uuid.New().String(),
		RequestID:  requestID,
		ProviderID: resp.ID,
		Timestamp:  time.Now().UTC(),
		Model:      resp.Model,
		Provider:   provider,
		Endpoint:   endpoint,
	}

	// Extract usage if available
	if resp.Usage != nil {
		entry.InputTokens = resp.Usage.InputTokens
		entry.OutputTokens = resp.Usage.OutputTokens
		entry.TotalTokens = resp.Usage.TotalTokens

		// Preserve raw extended usage data if available (defensive copy to avoid races)
		if len(resp.Usage.RawUsage) > 0 {
			entry.RawData = cloneRawData(resp.Usage.RawUsage)
		}

		// Merge typed detail fields into RawData (non-streaming path).
		if entry.RawData == nil {
			entry.RawData = buildRawUsageFromDetails(resp.Usage.PromptTokensDetails, resp.Usage.CompletionTokensDetails)
		}
	}

	// Calculate granular costs if pricing is provided
	if len(pricing) > 0 && pricing[0] != nil {
		effectivePricing := pricingForEndpoint(pricing[0], endpoint)
		costResult := CalculateGranularCost(entry.InputTokens, entry.OutputTokens, entry.RawData, provider, effectivePricing)
		entry.InputCost = costResult.InputCost
		entry.OutputCost = costResult.OutputCost
		entry.TotalCost = costResult.TotalCost
		entry.CostsCalculationCaveat = costResult.Caveat
	}

	return entry
}

// ExtractFromEmbeddingResponse extracts usage data from an EmbeddingResponse.
// Embeddings only have prompt tokens (no output tokens).
// For `/v1/batches` endpoints (exact or subpath), BatchInputPerMtok may replace
// standard InputPerMtok when pricingForEndpoint applies batch overrides.
func ExtractFromEmbeddingResponse(resp *core.EmbeddingResponse, requestID, provider, endpoint string, pricing ...*core.ModelPricing) *UsageEntry {
	if resp == nil {
		return nil
	}

	entry := &UsageEntry{
		ID:          uuid.New().String(),
		RequestID:   requestID,
		Timestamp:   time.Now().UTC(),
		Model:       resp.Model,
		Provider:    provider,
		Endpoint:    endpoint,
		InputTokens: resp.Usage.PromptTokens,
		TotalTokens: resp.Usage.TotalTokens,
	}

	if len(pricing) > 0 && pricing[0] != nil {
		effectivePricing := pricingForEndpoint(pricing[0], endpoint)
		costResult := CalculateGranularCost(entry.InputTokens, entry.OutputTokens, entry.RawData, provider, effectivePricing)
		entry.InputCost = costResult.InputCost
		entry.OutputCost = costResult.OutputCost
		entry.TotalCost = costResult.TotalCost
		entry.CostsCalculationCaveat = costResult.Caveat
	}

	return entry
}

// ExtractFromSSEUsage creates a UsageEntry from SSE-extracted usage data.
// This is used for streaming responses where usage is extracted from the final SSE event.
// If pricing is provided, cost fields are calculated.
// For `/v1/batches` endpoints (exact or subpath), batch pricing overrides
// (BatchInputPerMtok/BatchOutputPerMtok) may replace standard input/output rates.
func ExtractFromSSEUsage(
	providerID string,
	inputTokens, outputTokens, totalTokens int,
	rawData map[string]any,
	requestID, model, provider, endpoint string,
	pricing ...*core.ModelPricing,
) *UsageEntry {
	entry := &UsageEntry{
		ID:           uuid.New().String(),
		RequestID:    requestID,
		ProviderID:   providerID,
		Timestamp:    time.Now().UTC(),
		Model:        model,
		Provider:     provider,
		Endpoint:     endpoint,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
	}

	// Defensive copy to avoid races when original map might be mutated
	if len(rawData) > 0 {
		entry.RawData = cloneRawData(rawData)
	}

	// Calculate granular costs if pricing is provided
	if len(pricing) > 0 && pricing[0] != nil {
		effectivePricing := pricingForEndpoint(pricing[0], endpoint)
		costResult := CalculateGranularCost(entry.InputTokens, entry.OutputTokens, entry.RawData, provider, effectivePricing)
		entry.InputCost = costResult.InputCost
		entry.OutputCost = costResult.OutputCost
		entry.TotalCost = costResult.TotalCost
		entry.CostsCalculationCaveat = costResult.Caveat
	}

	return entry
}

// ExtractFromCachedResponseBody converts a cached OpenAI-compatible response body into
// a synthetic usage entry for a cache hit. If the response body cannot be parsed, it
// still returns a minimal zero-token entry so cache-hit counts remain observable.
func ExtractFromCachedResponseBody(
	body []byte,
	requestID, model, provider, endpoint, cacheType string,
	pricing ...*core.ModelPricing,
) *UsageEntry {
	cacheType = normalizeCacheType(cacheType)
	if cacheType == "" {
		cacheType = CacheTypeExact
	}
	endpoint = normalizeCachedResponseEndpoint(endpoint)

	var entry *UsageEntry
	switch endpoint {
	case "/v1/chat/completions":
		var resp core.ChatResponse
		if err := json.Unmarshal(body, &resp); err == nil {
			entry = ExtractFromChatResponse(&resp, requestID, provider, endpoint, pricing...)
		}
	case "/v1/responses":
		var resp core.ResponsesResponse
		if err := json.Unmarshal(body, &resp); err == nil {
			entry = ExtractFromResponsesResponse(&resp, requestID, provider, endpoint, pricing...)
		}
	case "/v1/embeddings":
		var resp core.EmbeddingResponse
		if err := json.Unmarshal(body, &resp); err == nil {
			entry = ExtractFromEmbeddingResponse(&resp, requestID, provider, endpoint, pricing...)
		}
	}

	if entry == nil {
		entry = &UsageEntry{
			ID:         uuid.New().String(),
			RequestID:  requestID,
			Timestamp:  time.Now().UTC(),
			Model:      model,
			Provider:   provider,
			Endpoint:   endpoint,
			CacheType:  cacheType,
			ProviderID: "",
		}
		return entry
	}

	entry.CacheType = cacheType
	if normalized := strings.TrimSpace(requestID); normalized != "" {
		entry.RequestID = normalized
	}
	if normalized := strings.TrimSpace(model); normalized != "" {
		entry.Model = normalized
	}
	if normalized := strings.TrimSpace(provider); normalized != "" {
		entry.Provider = normalized
	}
	if normalized := strings.TrimSpace(endpoint); normalized != "" {
		entry.Endpoint = normalized
	}
	return entry
}

func normalizeCachedResponseEndpoint(endpoint string) string {
	normalized := strings.TrimSpace(endpoint)
	if normalized == "" {
		return ""
	}

	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return ""
	}
	if strings.HasPrefix(normalized, "/") && !strings.HasPrefix(cleaned, "/") {
		return "/" + cleaned
	}
	return cleaned
}

func pricingForEndpoint(pricing *core.ModelPricing, endpoint string) *core.ModelPricing {
	if pricing == nil {
		return nil
	}
	if endpoint != "/v1/batches" && !strings.HasPrefix(endpoint, "/v1/batches/") {
		return pricing
	}

	effective := *pricing
	if pricing.BatchInputPerMtok != nil {
		effective.InputPerMtok = pricing.BatchInputPerMtok
	}
	if pricing.BatchOutputPerMtok != nil {
		effective.OutputPerMtok = pricing.BatchOutputPerMtok
	}
	return &effective
}
