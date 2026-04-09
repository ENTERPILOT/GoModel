package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

var batchResultsPending404Providers = map[string]struct{}{
	"anthropic": {},
}

type batchExecutionSelection struct {
	providerType string
	selector     core.ExecutionPlanSelector
}

func determineBatchExecutionSelection(
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	req *core.BatchRequest,
) (batchExecutionSelection, error) {
	return determineBatchExecutionSelectionWithAuthorizer(context.Background(), provider, resolver, nil, req)
}

func determineBatchExecutionSelectionWithAuthorizer(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	authorizer RequestModelAuthorizer,
	req *core.BatchRequest,
) (batchExecutionSelection, error) {
	if provider == nil {
		return batchExecutionSelection{}, core.NewInvalidRequestError("provider is not configured", nil)
	}

	if strings.TrimSpace(req.InputFileID) != "" {
		if req.Metadata == nil {
			return batchExecutionSelection{}, core.NewInvalidRequestError("metadata.provider is required for input_file_id batches", nil)
		}
		providerType := strings.TrimSpace(req.Metadata["provider"])
		if providerType == "" {
			return batchExecutionSelection{}, core.NewInvalidRequestError("metadata.provider is required for input_file_id batches", nil)
		}
		return batchExecutionSelection{
			providerType: providerType,
			selector:     core.NewExecutionPlanSelector(providerType, ""),
		}, nil
	}

	if len(req.Requests) == 0 {
		return batchExecutionSelection{}, core.NewInvalidRequestError("requests is required and must not be empty", nil)
	}

	var (
		providerType   string
		commonModel    string
		hasCommonModel = true
	)
	for i, item := range req.Requests {
		requested, err := core.BatchItemRequestedModelSelector(req.Endpoint, item)
		if err != nil {
			return batchExecutionSelection{}, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}
		resolvedSelector, _, err := resolveExecutionSelector(provider, resolver, requested)
		if err != nil {
			return batchExecutionSelection{}, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}
		model := resolvedSelector.QualifiedModel()
		if model == "" {
			return batchExecutionSelection{}, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: model is required", i), nil)
		}
		if !provider.Supports(model) {
			return batchExecutionSelection{}, core.NewInvalidRequestError("unsupported model: "+model, nil)
		}
		if authorizer != nil {
			if err := authorizer.ValidateModelAccess(ctx, resolvedSelector); err != nil {
				return batchExecutionSelection{}, err
			}
		}
		itemProvider := provider.GetProviderType(model)
		if providerType == "" {
			providerType = itemProvider
		} else if providerType != itemProvider {
			return batchExecutionSelection{}, core.NewInvalidRequestError("native batch supports a single provider per batch; split mixed-provider requests", nil)
		}

		if !hasCommonModel {
			continue
		}
		resolvedModel := strings.TrimSpace(resolvedSelector.Model)
		if resolvedModel == "" {
			hasCommonModel = false
			continue
		}
		if commonModel == "" {
			commonModel = resolvedModel
			continue
		}
		if commonModel != resolvedModel {
			hasCommonModel = false
		}
	}

	if providerType == "" {
		return batchExecutionSelection{}, core.NewInvalidRequestError("unable to resolve provider for batch", nil)
	}
	if !hasCommonModel {
		commonModel = ""
	}
	return batchExecutionSelection{
		providerType: providerType,
		selector:     core.NewExecutionPlanSelector(providerType, commonModel),
	}, nil
}

func mergeBatchRequestEndpointHints(left, right map[string]string) map[string]string {
	if len(left) == 0 {
		if len(right) == 0 {
			return nil
		}
		merged := make(map[string]string, len(right))
		maps.Copy(merged, right)
		return merged
	}

	merged := make(map[string]string, len(left))
	maps.Copy(merged, left)
	maps.Copy(merged, right)
	return merged
}

func (h *Handler) cleanupPreparedBatchInputFile(ctx context.Context, providerType, fileID string) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return
	}
	files, ok := h.provider.(core.NativeFileRoutableProvider)
	if !ok {
		return
	}
	if _, err := files.DeleteFile(ctx, providerType, fileID); err != nil {
		slog.Warn("failed to delete rewritten batch input file", "provider", providerType, "file_id", fileID, "error", err)
	}
}

func (h *Handler) loadBatch(c *echo.Context, id string) (*batchstore.StoredBatch, error) {
	resp, err := h.batchStore.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return nil, core.NewNotFoundError("batch not found: " + id)
		}
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to load batch", err)
	}
	if resp.Batch != nil {
		auditlog.EnrichEntry(c, "batch", resp.Batch.Provider)
	}
	return resp, nil
}

func (h *Handler) logBatchUsageFromBatchResults(stored *batchstore.StoredBatch, result *core.BatchResultsResponse, fallbackRequestID string) bool {
	if h.usageLogger == nil || !h.usageLogger.Config().Enabled || stored == nil || stored.Batch == nil || result == nil || len(result.Data) == 0 {
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
	hasAnyCost := false

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

		inputTokens, outputTokens, usageTotal, hasUsage := extractTokenTotals(usagePayload)
		if !hasUsage {
			continue
		}

		provider := firstNonEmpty(item.Provider, stored.Batch.Provider)
		model := firstNonEmpty(item.Model, stringFromAny(payload["model"]))
		providerID := firstNonEmpty(
			stringFromAny(payload["id"]),
			item.CustomID,
			fmt.Sprintf("%s:%d", firstNonEmpty(stored.Batch.ProviderBatchID, stored.Batch.ID), item.Index),
		)
		rawUsage := buildBatchUsageRawData(usagePayload, stored.Batch, item)

		var pricing *core.ModelPricing
		if h.pricingResolver != nil && model != "" {
			pricing = h.pricingResolver.ResolvePricing(model, provider)
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

		h.usageLogger.Write(entry)
		loggedEntries++
		inputTotal += inputTokens
		outputTotal += outputTokens
		totalTokens += usageTotal
		if entry.InputCost != nil {
			inputCostTotal += *entry.InputCost
			hasAnyCost = true
		}
		if entry.OutputCost != nil {
			outputCostTotal += *entry.OutputCost
			hasAnyCost = true
		}
		if entry.TotalCost != nil {
			totalCostTotal += *entry.TotalCost
			hasAnyCost = true
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
	if hasAnyCost {
		stored.Batch.Usage.InputCost = &inputCostTotal
		stored.Batch.Usage.OutputCost = &outputCostTotal
		stored.Batch.Usage.TotalCost = &totalCostTotal
	}

	return true
}

func deterministicBatchUsageID(stored *core.BatchResponse, item core.BatchResultItem, providerID string) string {
	seed := fmt.Sprintf(
		"%s|%s|%d|%s|%s",
		firstNonEmpty(stored.ID, stored.ProviderBatchID),
		firstNonEmpty(stored.ProviderBatchID, stored.ID),
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

func extractTokenTotals(usagePayload map[string]any) (int, int, int, bool) {
	inputTokens, hasInput := readFirstInt(usagePayload, "input_tokens", "prompt_tokens")
	outputTokens, hasOutput := readFirstInt(usagePayload, "output_tokens", "completion_tokens")
	totalTokens, hasTotal := readFirstInt(usagePayload, "total_tokens")
	if !hasTotal && (hasInput || hasOutput) {
		totalTokens = inputTokens + outputTokens
		hasTotal = true
	}

	return inputTokens, outputTokens, totalTokens, hasInput || hasOutput || hasTotal
}

func readFirstInt(values map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if num, ok := intFromAny(value); ok {
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
	maxInt := float64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if v < minInt || v > maxInt {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mergeStoredBatchFromUpstream(stored *batchstore.StoredBatch, upstream *core.BatchResponse) {
	if stored == nil || stored.Batch == nil || upstream == nil {
		return
	}

	stored.Batch.Status = firstNonEmpty(upstream.Status, stored.Batch.Status)
	stored.Batch.Endpoint = firstNonEmpty(upstream.Endpoint, stored.Batch.Endpoint)
	if strings.TrimSpace(stored.Batch.InputFileID) == "" {
		stored.Batch.InputFileID = firstNonEmpty(stored.OriginalInputFileID, upstream.InputFileID)
	}
	stored.Batch.CompletionWindow = firstNonEmpty(upstream.CompletionWindow, stored.Batch.CompletionWindow)
	if hasBatchRequestCounts(upstream.RequestCounts) {
		stored.Batch.RequestCounts = upstream.RequestCounts
	}
	if hasBatchUsageSummary(upstream.Usage) {
		stored.Batch.Usage = upstream.Usage
	}
	if len(upstream.Results) > 0 {
		stored.Batch.Results = upstream.Results
	}
	if upstream.InProgressAt != nil {
		stored.Batch.InProgressAt = upstream.InProgressAt
	}
	if upstream.CompletedAt != nil {
		stored.Batch.CompletedAt = upstream.CompletedAt
	}
	if upstream.FailedAt != nil {
		stored.Batch.FailedAt = upstream.FailedAt
	}
	if upstream.CancellingAt != nil {
		stored.Batch.CancellingAt = upstream.CancellingAt
	}
	if upstream.CancelledAt != nil {
		stored.Batch.CancelledAt = upstream.CancelledAt
	}
	if upstream.Metadata != nil {
		if stored.Batch.Metadata == nil {
			stored.Batch.Metadata = map[string]string{}
		}
		preservedGatewayMetadata := map[string]string{}
		for _, key := range []string{"provider", "provider_batch_id"} {
			if value, exists := stored.Batch.Metadata[key]; exists {
				preservedGatewayMetadata[key] = value
			}
		}
		for key, value := range upstream.Metadata {
			if _, preserve := preservedGatewayMetadata[key]; preserve {
				continue
			}
			stored.Batch.Metadata[key] = value
		}
		maps.Copy(stored.Batch.Metadata, preservedGatewayMetadata)
		stored.Batch.Metadata = sanitizePublicBatchMetadata(stored.Batch.Metadata)
	}
}

func hasBatchRequestCounts(counts core.BatchRequestCounts) bool {
	return counts.Total != 0 || counts.Completed != 0 || counts.Failed != 0
}

func hasBatchUsageSummary(usage core.BatchUsageSummary) bool {
	return usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.TotalTokens != 0 ||
		usage.InputCost != nil ||
		usage.OutputCost != nil ||
		usage.TotalCost != nil
}

func isTerminalBatchStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled", "expired":
		return true
	default:
		return false
	}
}

func (h *Handler) cleanupStoredBatchRewrittenInputFile(ctx context.Context, stored *batchstore.StoredBatch) bool {
	if stored == nil || stored.Batch == nil {
		return false
	}
	fileID := strings.TrimSpace(stored.RewrittenInputFileID)
	if fileID == "" {
		return false
	}
	nativeFiles, err := h.nativeFiles().router()
	if err != nil {
		return false
	}
	if _, err := nativeFiles.DeleteFile(ctx, stored.Batch.Provider, fileID); err != nil {
		slog.Warn(
			"failed to delete rewritten batch input file",
			"batch_id", stored.Batch.ID,
			"provider", stored.Batch.Provider,
			"file_id", fileID,
			"error", err,
		)
		return false
	}
	stored.RewrittenInputFileID = ""
	return true
}

func isNativeBatchResultsPending(
	ctx context.Context,
	nativeRouter core.NativeBatchRoutableProvider,
	providerType, providerBatchID string,
	err error,
) (bool, *core.BatchResponse) {
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	if !ok {
		return false, nil
	}
	if gatewayErr.HTTPStatusCode() != http.StatusNotFound {
		return false, nil
	}
	// Some providers return 404 while native results are still being prepared.
	// Extend batchResultsPending404Providers as more provider-specific behaviors are confirmed.
	if _, ok := batchResultsPending404Providers[strings.ToLower(strings.TrimSpace(gatewayErr.Provider))]; !ok {
		return false, nil
	}
	if nativeRouter == nil || strings.TrimSpace(providerType) == "" || strings.TrimSpace(providerBatchID) == "" {
		return false, nil
	}
	latest, getErr := nativeRouter.GetBatch(ctx, providerType, providerBatchID)
	if getErr != nil || latest == nil || isTerminalBatchStatus(latest.Status) {
		return false, latest
	}
	return true, latest
}
