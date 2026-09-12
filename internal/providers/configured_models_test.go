package providers

import (
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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

	require.Equal(t, configuredProviderModelsAllowlist, reason)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, int64(123), resp.Data[0].Created)
	require.Equal(t, "upstream", resp.Data[0].OwnedBy)
}

func TestApplyConfiguredProviderModels_MergeAppendsMissingModels(t *testing.T) {
	upstream := &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			// Padded ID: the retained entry must be normalized, not just deduped.
			{ID: " listed-model ", Object: "model", OwnedBy: "upstream", Created: 42},
			// A whitespace variant of the same ID must not create a duplicate.
			{ID: "listed-model", Object: "model", OwnedBy: "upstream-dup", Created: 43},
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

	require.Equal(t, configuredProviderModelsMerge, reason)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 2)
	require.Equal(t, "listed-model", resp.Data[0].ID)
	require.Equal(t, "upstream", resp.Data[0].OwnedBy)
	require.Equal(t, int64(42), resp.Data[0].Created, "Data[0] = %+v, want upstream entry kept authoritative", resp.Data[0])
	require.Equal(t, "unlisted-model", resp.Data[1].ID)
	require.Equal(t, "test-type", resp.Data[1].OwnedBy)
	require.Equal(t, int64(123), resp.Data[1].Created, "Data[1] = %+v, want synthesized configured entry", resp.Data[1])
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
			require.Equal(t, tt.wantReason, reason)
			require.NotNil(t, resp)
			require.Len(t, resp.Data, 1)
			require.Equal(t, "configured-model", resp.Data[0].ID)
		})
	}
}
