package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestService_RenameMovesRedirectToNewSource(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.NoError(t, err)
	err = svc.Rename(ctx, "fast", VirtualModel{
		Source:  "speedy",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.NoError(t, err)
	// The old source is gone from the store and the snapshot.
	_, err = svc.store.Get(ctx, "fast")
	require.Equal(t, ErrNotFound, err)
	_, ok := svc.Get("fast")
	require.False(t, ok)

	// The new source resolves to the same target.
	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("speedy", ""))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel())
}

func TestService_RenamePreservesDisabledState(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: false,
	})
	require.NoError(t, err)
	err = svc.Rename(ctx, "fast", VirtualModel{
		Source:  "speedy",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: false,
	})
	require.NoError(t, err)

	stored, err := svc.store.Get(ctx, "speedy")
	require.NoError(t, err)
	require.False(t, stored.Enabled, "rename flipped a disabled redirect to enabled: %#v", stored)
}

func TestService_RenameToNoopPolicyDeletesOldSource(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.NoError(t, err)
	err = svc.Rename(ctx, "fast", VirtualModel{Source: "gpt-4o", Enabled: true})
	require.NoError(t, err)
	_, ok := svc.Get("fast")
	require.False(t, ok)
	_, ok = svc.Get("gpt-4o")
	require.False(t, ok)
}

func TestService_RenameRejectsExistingTarget(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)
	err = svc.Upsert(ctx, VirtualModel{Source: "taken", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)

	err = svc.Rename(ctx, "fast", VirtualModel{Source: "taken", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	// Both rows survive intact: neither clobbered, no orphan removed.
	_, getErr := svc.store.Get(ctx, "fast")
	require.NoError(t, getErr)
	_, getErr = svc.store.Get(ctx, "taken")
	require.NoError(t, getErr)
}

func TestService_RenameMissingSourceReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	err := svc.Rename(context.Background(), "nope", VirtualModel{
		Source:  "somewhere",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.Equal(t, ErrNotFound, err)
}

func TestService_RenameSameSourceActsAsUpsert(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)
	// A no-op rename (old == new) updates in place without deleting the row.
	err = svc.Rename(ctx, "fast", VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Description: "updated", Enabled: true})
	require.NoError(t, err)

	stored, err := svc.store.Get(ctx, "fast")
	require.NoError(t, err)
	require.Equal(t, "updated", stored.Description, "no-op rename did not update the row: %#v", stored)
}

func TestService_RenameRejectsManagedSource(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []config.VirtualModelTargetConfig{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "groq", Model: "llama"},
		},
	}}))
	err := svc.Refresh(ctx)
	require.NoError(t, err)

	err = svc.Rename(ctx, "smart", VirtualModel{
		Source:   "renamed",
		Strategy: StrategyRoundRobin,
		Targets:  []Target{{Provider: "openai", Model: "gpt-4o"}, {Provider: "groq", Model: "llama"}},
		Enabled:  true,
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}
