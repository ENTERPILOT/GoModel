package providers

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestEnsureProviderBatchID(t *testing.T) {
	t.Run("defaults to id when empty", func(t *testing.T) {
		resp := &core.BatchResponse{ID: "batch_1"}
		EnsureProviderBatchID(resp)
		if resp.ProviderBatchID != "batch_1" {
			t.Errorf("ProviderBatchID = %q, want %q", resp.ProviderBatchID, "batch_1")
		}
	})

	t.Run("preserves existing provider id", func(t *testing.T) {
		resp := &core.BatchResponse{ID: "batch_1", ProviderBatchID: "upstream_9"}
		EnsureProviderBatchID(resp)
		if resp.ProviderBatchID != "upstream_9" {
			t.Errorf("ProviderBatchID = %q, want %q", resp.ProviderBatchID, "upstream_9")
		}
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

	if resp.Data[0].ProviderBatchID != "batch_1" {
		t.Errorf("Data[0].ProviderBatchID = %q, want %q", resp.Data[0].ProviderBatchID, "batch_1")
	}
	if resp.Data[1].ProviderBatchID != "upstream_2" {
		t.Errorf("Data[1].ProviderBatchID = %q, want %q", resp.Data[1].ProviderBatchID, "upstream_2")
	}

	EnsureProviderBatchIDs(nil) // must not panic
}
