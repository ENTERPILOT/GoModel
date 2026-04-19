package gateway

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// LogBatchUsageFromBatchResults writes per-item usage from batch results once.
func LogBatchUsageFromBatchResults(
	stored *batchstore.StoredBatch,
	result *core.BatchResultsResponse,
	fallbackRequestID string,
	usageLogger usage.LoggerInterface,
	pricingResolver usage.PricingResolver,
) bool {
	if usageLogger == nil || !usageLogger.Config().Enabled || stored == nil || stored.Batch == nil || result == nil || len(result.Data) == 0 {
		return false
	}
	if !stored.EffectiveUsageEnabled() {
		return false
	}
	if stored.UsageLoggedAt != nil {
		return false
	}

	requestID := strings.TrimSpace(stored.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(fallbackRequestID)
	}
	if requestID == "" {
		requestID = "batch:" + stored.Batch.ID
	}

	loggedEntries := 0
	inputTotal := 0
	outputTotal := 0
	totalTokens := 0
	var inputCostTotal float64
	var outputCostTotal float64
	var totalCostTotal float64
	hasInputCost := false
	hasOutputCost := false
	hasTotalCost := false

	for _, item := range result.Data {
		if item.StatusCode < http.StatusOK || item.StatusCode >= http.StatusMultipleChoices {
			continue
		}

		payload, ok := asJSONMap(item.Response)
		if !ok {
			continue
		}
		usagePayload, ok := asJSONMap(payload["usage"])
		if !ok {
			continue
		}

		inputTokens, outputTokens, usageTotal, hasUsage, hasTotal := extractTokenTotals(usagePayload)
		if !hasUsage {
			continue
		}

		provider := FirstNonEmpty(item.Provider, stored.Batch.Provider)
		model := FirstNonEmpty(item.Model, stringFromAny(payload["model"]))
		providerID := FirstNonEmpty(
			stringFromAny(payload["id"]),
			item.CustomID,
			fmt.Sprintf("%s:%d", FirstNonEmpty(stored.Batch.ProviderBatchID, stored.Batch.ID), item.Index),
		)
		rawUsage := buildBatchUsageRawData(usagePayload, stored.Batch, item)

		var pricing *core.ModelPricing
		if pricingResolver != nil && model != "" {
			pricing = pricingResolver.ResolvePricing(model, provider)
		}

		entry := usage.ExtractFromSSEUsage(
			providerID,
			inputTokens,
			outputTokens,
			usageTotal,
			rawUsage,
			requestID,
			model,
			provider,
			"/v1/batches",
			pricing,
		)
		if entry == nil {
			continue
		}
		entry.ID = deterministicBatchUsageID(stored.Batch, item, providerID)
		entry.UserPath = stored.UserPath

		usageLogger.Write(entry)
		loggedEntries++
		inputTotal += inputTokens
		outputTotal += outputTokens
		if hasTotal {
			totalTokens += usageTotal
		}
		if entry.InputCost != nil {
			inputCostTotal += *entry.InputCost
			hasInputCost = true
		}
		if entry.OutputCost != nil {
			outputCostTotal += *entry.OutputCost
			hasOutputCost = true
		}
		if entry.TotalCost != nil {
			totalCostTotal += *entry.TotalCost
			hasTotalCost = true
		}
	}

	if loggedEntries == 0 {
		return false
	}

	now := time.Now().UTC()
	stored.RequestID = requestID
	stored.UsageLoggedAt = &now

	stored.Batch.Usage.InputTokens = inputTotal
	stored.Batch.Usage.OutputTokens = outputTotal
	stored.Batch.Usage.TotalTokens = totalTokens
	if hasInputCost {
		stored.Batch.Usage.InputCost = &inputCostTotal
	}
	if hasOutputCost {
		stored.Batch.Usage.OutputCost = &outputCostTotal
	}
	if hasTotalCost {
		stored.Batch.Usage.TotalCost = &totalCostTotal
	}

	return true
}

func deterministicBatchUsageID(stored *core.BatchResponse, item core.BatchResultItem, providerID string) string {
	seed := fmt.Sprintf(
		"%s|%s|%d|%s|%s",
		FirstNonEmpty(stored.ID, stored.ProviderBatchID),
		FirstNonEmpty(stored.ProviderBatchID, stored.ID),
		item.Index,
		item.CustomID,
		providerID,
	)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

func buildBatchUsageRawData(usagePayload map[string]any, stored *core.BatchResponse, item core.BatchResultItem) map[string]any {
	if usagePayload == nil {
		return nil
	}

	raw := make(map[string]any)
	for key, value := range usagePayload {
		switch key {
		case "input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "total_tokens":
			continue
		default:
			raw[key] = value
		}
	}

	if promptDetails, ok := asJSONMap(usagePayload["prompt_tokens_details"]); ok {
		for key, value := range promptDetails {
			raw["prompt_"+key] = value
		}
	}
	if completionDetails, ok := asJSONMap(usagePayload["completion_tokens_details"]); ok {
		for key, value := range completionDetails {
			raw["completion_"+key] = value
		}
	}

	raw["batch_id"] = stored.ID
	raw["provider_batch_id"] = stored.ProviderBatchID
	raw["batch_result_index"] = item.Index
	if item.CustomID != "" {
		raw["batch_custom_id"] = item.CustomID
	}
	if endpoint := strings.TrimSpace(stored.Endpoint); endpoint != "" {
		raw["batch_endpoint"] = endpoint
	}

	return raw
}

func extractTokenTotals(usagePayload map[string]any) (inputTokens, outputTokens, totalTokens int, hasUsage, hasTotal bool) {
	var hasInput, hasOutput bool
	inputTokens, hasInput = readFirstInt(usagePayload, "input_tokens", "prompt_tokens")
	outputTokens, hasOutput = readFirstInt(usagePayload, "output_tokens", "completion_tokens")
	totalTokens, hasTotal = readFirstInt(usagePayload, "total_tokens")
	if !hasTotal && hasInput && hasOutput {
		totalTokens = inputTokens + outputTokens
		hasTotal = true
	}

	return inputTokens, outputTokens, totalTokens, hasInput || hasOutput || hasTotal, hasTotal
}

func readFirstInt(values map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if num, ok := intFromAny(value); ok {
			if num < 0 {
				continue
			}
			return num, true
		}
	}
	return 0, false
}

func intFromAny(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return intFromInt64(v)
	case uint:
		return intFromUint64(uint64(v))
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return intFromUint64(uint64(v))
	case uint64:
		return intFromUint64(v)
	case float32:
		return intFromFloat64(float64(v))
	case float64:
		return intFromFloat64(v)
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return intFromInt64(i)
		}
		f, err := v.Float64()
		if err == nil {
			return intFromFloat64(f)
		}
		return 0, false
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func intFromInt64(v int64) (int, bool) {
	maxInt := int64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if v < minInt || v > maxInt {
		return 0, false
	}
	return int(v), true
}

func intFromUint64(v uint64) (int, bool) {
	maxInt := uint64(^uint(0) >> 1)
	if v > maxInt {
		return 0, false
	}
	return int(v), true
}

func intFromFloat64(v float64) (int, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	const (
		intBits   = strconv.IntSize
		maxIntVal = 1<<(intBits-1) - 1
		minIntVal = -1 << (intBits - 1)
	)
	maxInt := float64(maxIntVal)
	minInt := float64(minIntVal)
	if v < minInt || v > maxInt {
		return 0, false
	}
	if intBits == 64 && v == maxInt {
		return 0, false
	}
	if math.Trunc(v) != v {
		return 0, false
	}
	return int(v), true
}

func asJSONMap(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		return decodeJSONMap(v)
	case []byte:
		return decodeJSONMap(v)
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, false
		}
		return decodeJSONMap([]byte(v))
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return decodeJSONMap(raw)
	}
}

func decodeJSONMap(raw []byte) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}
