package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
)

func TestEnsureProviderBatchID(t *testing.T) {
	t.Run("defaults to id when empty", func(t *testing.T) {
		resp := &core.BatchResponse{ID: "batch_1"}
		EnsureProviderBatchID(resp)
		assert.Equal(t, "batch_1", resp.ProviderBatchID)
	})

	t.Run("preserves existing provider id", func(t *testing.T) {
		resp := &core.BatchResponse{ID: "batch_1", ProviderBatchID: "upstream_9"}
		EnsureProviderBatchID(resp)
		assert.Equal(t, "upstream_9", resp.ProviderBatchID)
	})

	t.Run("nil is a no-op", func(t *testing.T) {
		EnsureProviderBatchID(nil) // must not panic
	})
}

func TestEnsureProviderBatchIDs(t *testing.T) {
	resp := &core.BatchListResponse{
		Data: []core.BatchResponse{
			{ID: "batch_1"},
			{ID: "batch_2", ProviderBatchID: "upstream_2"},
		},
	}
	EnsureProviderBatchIDs(resp)

	assert.Equal(t, "batch_1", resp.Data[0].ProviderBatchID)
	assert.Equal(t, "upstream_2", resp.Data[1].ProviderBatchID)

	EnsureProviderBatchIDs(nil) // must not panic
}
