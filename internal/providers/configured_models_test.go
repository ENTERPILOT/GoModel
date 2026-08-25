package providers

import (
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
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

func TestApplyConfiguredProviderModels_MergeAppendsMissingModels(t *testing.T) {
	upstream := &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			// Padded ID: the retained entry must be normalized, not just deduped.
			{ID: " listed-model ", Object: "model", OwnedBy: "upstream", Created: 42},
		},
	}
	resp, reason := applyConfiguredProviderModels(
		"test",
		"test-type",
		config.ConfiguredProviderModelsModeMerge,
		[]string{"listed-model", "unlisted-model"},
		upstream,
		nil,
		123,
	)

	if reason != configuredProviderModelsMerge {
		t.Fatalf("reason = %q, want %q", reason, configuredProviderModelsMerge)
	}
	if resp == nil || len(resp.Data) != 2 {
		t.Fatalf("resp = %+v, want upstream model plus one synthesized entry", resp)
	}
	if resp.Data[0].ID != "listed-model" || resp.Data[0].OwnedBy != "upstream" || resp.Data[0].Created != 42 {
		t.Fatalf("Data[0] = %+v, want upstream entry kept authoritative", resp.Data[0])
	}
	if resp.Data[1].ID != "unlisted-model" || resp.Data[1].OwnedBy != "test-type" || resp.Data[1].Created != 123 {
		t.Fatalf("Data[1] = %+v, want synthesized configured entry", resp.Data[1])
	}
}

func TestApplyConfiguredProviderModels_MergeFallsBackWhenUpstreamFails(t *testing.T) {
	tests := []struct {
		name       string
		upstream   *core.ModelsResponse
		err        error
		wantReason configuredProviderModelsApplyReason
	}{
		{name: "error", upstream: nil, err: errors.New("upstream down"), wantReason: configuredProviderModelsUpstreamError},
		{name: "nil", upstream: nil, wantReason: configuredProviderModelsUpstreamNil},
		{name: "empty", upstream: &core.ModelsResponse{Object: "list"}, wantReason: configuredProviderModelsUpstreamEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, reason := applyConfiguredProviderModels(
				"test",
				"test-type",
				config.ConfiguredProviderModelsModeMerge,
				[]string{"configured-model"},
				tt.upstream,
				tt.err,
				123,
			)
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
			if resp == nil || len(resp.Data) != 1 || resp.Data[0].ID != "configured-model" {
				t.Fatalf("resp = %+v, want configured fallback inventory", resp)
			}
		})
	}
}
