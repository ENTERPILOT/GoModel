package providers

import (
	"testing"

	"gomodel/config"
	"gomodel/internal/core"
)

func TestApplyConfiguredProviderModels_BackfillsZeroCreatedForUpstreamMatch(t *testing.T) {
	resp, reason := applyConfiguredProviderModels(
		"test",
		"test-type",
		config.ConfiguredProviderModelsModeAllowlist,
		[]string{"configured-model"},
		&core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "configured-model", Object: "model", OwnedBy: "upstream"},
			},
		},
		nil,
		123,
	)

	if reason != configuredProviderModelsAllowlist {
		t.Fatalf("reason = %q, want %q", reason, configuredProviderModelsAllowlist)
	}
	if resp == nil || len(resp.Data) != 1 {
		t.Fatalf("resp = %+v, want one configured model", resp)
	}
	if resp.Data[0].Created != 123 {
		t.Fatalf("Created = %d, want fallback timestamp 123", resp.Data[0].Created)
	}
	if resp.Data[0].OwnedBy != "upstream" {
		t.Fatalf("OwnedBy = %q, want upstream metadata preserved", resp.Data[0].OwnedBy)
	}
}
