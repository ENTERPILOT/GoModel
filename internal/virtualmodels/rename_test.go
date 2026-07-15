package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestService_RenameMovesRedirectToNewSource(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	if err := svc.Rename(ctx, "fast", VirtualModel{
		Source:  "speedy",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	// The old source is gone from the store and the snapshot.
	if _, err := svc.store.Get(ctx, "fast"); err != ErrNotFound {
		t.Fatalf("store.Get(old) error = %v, want ErrNotFound", err)
	}
	if _, ok := svc.Get("fast"); ok {
		t.Fatalf("old source still resolves after rename")
	}

	// The new source resolves to the same target.
	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("speedy", ""))
	if err != nil {
		t.Fatalf("ResolveModel(new) error = %v", err)
	}
	if !changed || sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("ResolveModel(new) = %q changed=%v, want openai/gpt-4o true", sel.QualifiedModel(), changed)
	}
}

func TestService_RenamePreservesDisabledState(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: false,
	}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	if err := svc.Rename(ctx, "fast", VirtualModel{
		Source:  "speedy",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: false,
	}); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	stored, err := svc.store.Get(ctx, "speedy")
	if err != nil {
		t.Fatalf("store.Get(new) error = %v", err)
	}
	if stored.Enabled {
		t.Fatalf("rename flipped a disabled redirect to enabled: %#v", stored)
	}
}

func TestService_RenameRejectsExistingTarget(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(fast) error = %v", err)
	}
	if err := svc.Upsert(ctx, VirtualModel{Source: "taken", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(taken) error = %v", err)
	}

	err := svc.Rename(ctx, "fast", VirtualModel{Source: "taken", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	if err == nil {
		t.Fatalf("Rename(onto existing) error = nil, want rejection")
	}
	if !IsValidationError(err) {
		t.Fatalf("Rename(onto existing) error = %v, want validation error", err)
	}

	// Both rows survive intact: neither clobbered, no orphan removed.
	if _, getErr := svc.store.Get(ctx, "fast"); getErr != nil {
		t.Fatalf("store.Get(fast) after rejected rename error = %v", getErr)
	}
	if _, getErr := svc.store.Get(ctx, "taken"); getErr != nil {
		t.Fatalf("store.Get(taken) after rejected rename error = %v", getErr)
	}
}

func TestService_RenameMissingSourceReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	err := svc.Rename(context.Background(), "nope", VirtualModel{
		Source:  "somewhere",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	if err != ErrNotFound {
		t.Fatalf("Rename(missing) error = %v, want ErrNotFound", err)
	}
}

func TestService_RenameSameSourceActsAsUpsert(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	// A no-op rename (old == new) updates in place without deleting the row.
	if err := svc.Rename(ctx, "fast", VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Description: "updated", Enabled: true}); err != nil {
		t.Fatalf("Rename(same source) error = %v", err)
	}

	stored, err := svc.store.Get(ctx, "fast")
	if err != nil {
		t.Fatalf("store.Get(fast) error = %v", err)
	}
	if stored.Description != "updated" {
		t.Fatalf("no-op rename did not update the row: %#v", stored)
	}
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
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	err := svc.Rename(ctx, "smart", VirtualModel{
		Source:   "renamed",
		Strategy: StrategyRoundRobin,
		Targets:  []Target{{Provider: "openai", Model: "gpt-4o"}, {Provider: "groq", Model: "llama"}},
		Enabled:  true,
	})
	if err == nil {
		t.Fatalf("Rename(managed) error = nil, want rejection")
	}
	if !IsValidationError(err) {
		t.Fatalf("Rename(managed) error = %v, want validation error", err)
	}
}
