// Package batch provides persistence for OpenAI-compatible batch lifecycle endpoints.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gomodel/internal/core"
)

// ErrNotFound indicates a requested batch was not found.
var ErrNotFound = errors.New("batch not found")

const (
	// RequestIDMetadataKey is the legacy metadata key used to persist batch request IDs.
	RequestIDMetadataKey = "request_id"
	// UsageLoggedAtMetadataKey is the legacy metadata key used to mark logged batch usage.
	UsageLoggedAtMetadataKey = "usage_logged_at"
)

// StoredBatch keeps the public batch response separate from gateway-only
// persistence hints that should never be exposed by API DTOs.
type StoredBatch struct {
	Batch                     *core.BatchResponse `json:"batch"`
	RequestEndpointByCustomID map[string]string   `json:"request_endpoint_by_custom_id,omitempty"`
	OriginalInputFileID       string              `json:"original_input_file_id,omitempty"`
	RewrittenInputFileID      string              `json:"rewritten_input_file_id,omitempty"`
	RequestID                 string              `json:"request_id,omitempty"`
	ExecutionPlanVersionID    string              `json:"execution_plan_version_id,omitempty"`
	UsageEnabled              *bool               `json:"usage_enabled,omitempty"`
	UsageLoggedAt             *time.Time          `json:"usage_logged_at,omitempty"`
}

// Store defines persistence operations for batch lifecycle APIs.
type Store interface {
	Create(ctx context.Context, batch *StoredBatch) error
	Get(ctx context.Context, id string) (*StoredBatch, error)
	List(ctx context.Context, limit int, after string) ([]*StoredBatch, error)
	Update(ctx context.Context, batch *StoredBatch) error
	Close() error
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return 20
	case limit > 101:
		return 101
	default:
		return limit
	}
}

func cloneBatch(src *StoredBatch) (*StoredBatch, error) {
	if src == nil {
		return nil, fmt.Errorf("batch is nil")
	}
	normalized := normalizeStoredBatch(src)
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal batch: %w", err)
	}
	var dst StoredBatch
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}
	return &dst, nil
}

func serializeBatch(batch *StoredBatch) ([]byte, error) {
	if batch == nil {
		return nil, fmt.Errorf("batch is nil")
	}
	normalized := normalizeStoredBatch(batch)
	if normalized.Batch == nil {
		return nil, fmt.Errorf("batch payload is nil")
	}
	if len(normalized.Batch.ID) == 0 {
		return nil, fmt.Errorf("batch ID is empty")
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal batch: %w", err)
	}
	return b, nil
}

func deserializeBatch(raw []byte) (*StoredBatch, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty batch payload")
	}

	var stored StoredBatch
	if err := json.Unmarshal(raw, &stored); err == nil && stored.Batch != nil && stored.Batch.ID != "" {
		return normalizeStoredBatch(&stored), nil
	}

	var legacy core.BatchResponse
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}
	if legacy.ID == "" {
		return nil, fmt.Errorf("legacy batch missing ID")
	}
	return normalizeStoredBatch(&StoredBatch{Batch: &legacy}), nil
}

func normalizeStoredBatch(src *StoredBatch) *StoredBatch {
	if src == nil {
		return nil
	}

	normalized := *src
	if src.Batch == nil {
		return &normalized
	}

	batchCopy := *src.Batch
	batchCopy.Metadata, normalized.RequestID, normalized.UsageLoggedAt = splitGatewayBatchMetadata(
		src.Batch.Metadata,
		normalized.RequestID,
		normalized.UsageLoggedAt,
	)
	normalized.Batch = &batchCopy
	return &normalized
}

func splitGatewayBatchMetadata(metadata map[string]string, requestID string, usageLoggedAt *time.Time) (map[string]string, string, *time.Time) {
	if len(metadata) == 0 {
		return nil, strings.TrimSpace(requestID), usageLoggedAt
	}

	publicMetadata := make(map[string]string, len(metadata))
	normalizedRequestID := strings.TrimSpace(requestID)
	normalizedUsageLoggedAt := usageLoggedAt

	for key, value := range metadata {
		switch key {
		case RequestIDMetadataKey:
			if normalizedRequestID == "" {
				normalizedRequestID = strings.TrimSpace(value)
			}
		case UsageLoggedAtMetadataKey:
			if normalizedUsageLoggedAt == nil {
				normalizedUsageLoggedAt = parseUsageLoggedAt(value)
			}
		default:
			publicMetadata[key] = value
		}
	}

	if len(publicMetadata) == 0 {
		publicMetadata = nil
	}

	return publicMetadata, normalizedRequestID, normalizedUsageLoggedAt
}

func parseUsageLoggedAt(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	if unixSeconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		ts := time.Unix(unixSeconds, 0).UTC()
		return &ts
	}

	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		ts := parsed.UTC()
		return &ts
	}

	return nil
}

// EffectiveUsageEnabled reports whether batch usage logging should run.
// Nil means the value was not persisted by older versions, so usage remains enabled.
func (s *StoredBatch) EffectiveUsageEnabled() bool {
	return s == nil || s.UsageEnabled == nil || *s.UsageEnabled
}
