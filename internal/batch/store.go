// Package batch provides persistence for OpenAI-compatible batch lifecycle endpoints.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gomodel/internal/core"
)

// ErrNotFound indicates a requested batch was not found.
var ErrNotFound = errors.New("batch not found")

// StoredBatch keeps the public batch response separate from gateway-only
// persistence hints that should never be exposed by API DTOs.
type StoredBatch struct {
	Batch                     *core.BatchResponse `json:"batch"`
	RequestEndpointByCustomID map[string]string   `json:"request_endpoint_by_custom_id,omitempty"`
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
	b, err := json.Marshal(src)
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
	if batch.Batch == nil {
		return nil, fmt.Errorf("batch payload is nil")
	}
	if len(batch.Batch.ID) == 0 {
		return nil, fmt.Errorf("batch ID is empty")
	}
	b, err := json.Marshal(batch)
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
		return &stored, nil
	}

	var legacy core.BatchResponse
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}
	return &StoredBatch{Batch: &legacy}, nil
}
