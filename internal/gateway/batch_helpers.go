package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
)

var batchResultsPending404Providers = map[string]struct{}{
	"anthropic": {},
}

// SanitizePublicBatchMetadata removes gateway-private metadata keys.
func SanitizePublicBatchMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	publicMetadata := make(map[string]string, len(metadata))
	for key, value := range metadata {
		switch key {
		case batchstore.RequestIDMetadataKey, batchstore.UsageLoggedAtMetadataKey:
			continue
		default:
			publicMetadata[key] = value
		}
	}
	if len(publicMetadata) == 0 {
		return nil
	}
	return publicMetadata
}

// FirstNonEmpty returns the first non-empty trimmed string.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// MergeStoredBatchFromUpstream applies sparse upstream refresh fields to a stored batch.
func MergeStoredBatchFromUpstream(stored *batchstore.StoredBatch, upstream *core.BatchResponse) {
	if stored == nil || stored.Batch == nil || upstream == nil {
		return
	}

	stored.Batch.Status = FirstNonEmpty(upstream.Status, stored.Batch.Status)
	stored.Batch.Endpoint = FirstNonEmpty(upstream.Endpoint, stored.Batch.Endpoint)
	if strings.TrimSpace(stored.Batch.InputFileID) == "" {
		stored.Batch.InputFileID = FirstNonEmpty(stored.OriginalInputFileID, upstream.InputFileID)
	}
	stored.Batch.CompletionWindow = FirstNonEmpty(upstream.CompletionWindow, stored.Batch.CompletionWindow)
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

		gatewayMetadataKeys := map[string]struct{}{
			"provider":          {},
			"provider_batch_id": {},
		}
		for key, value := range upstream.Metadata {
			if _, owned := gatewayMetadataKeys[key]; owned {
				continue
			}
			stored.Batch.Metadata[key] = value
		}
		stored.Batch.Metadata = SanitizePublicBatchMetadata(stored.Batch.Metadata)
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

// IsTerminalBatchStatus reports whether status is terminal.
func IsTerminalBatchStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled", "expired":
		return true
	default:
		return false
	}
}

// IsNativeBatchResultsPending reports whether a provider 404 means results are still pending.
func IsNativeBatchResultsPending(
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
	if getErr != nil || latest == nil || IsTerminalBatchStatus(latest.Status) {
		return false, latest
	}
	return true, latest
}
