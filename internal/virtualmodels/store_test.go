package virtualmodels

import (
	"context"
	"errors"
	"testing"
)

func TestSQLiteStore_RoundTripRedirectAndPolicy(t *testing.T) {
	t.Parallel()
	store := newSQLiteVMStore(t)
	ctx := context.Background()

	redirect := VirtualModel{
		Source:      "fast",
		Targets:     []Target{{Provider: "openai", Model: "gpt-4o"}},
		Description: "primary",
		Enabled:     true,
	}
	policy := VirtualModel{
		Source:       "openai/gpt-4o",
		ProviderName: "openai",
		Model:        "gpt-4o",
		UserPaths:    []string{"/team"},
		Enabled:      true,
	}
	if err := store.Upsert(ctx, redirect); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}
	if err := store.Upsert(ctx, policy); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List()) = %d, want 2", len(got))
	}

	gotRedirect, err := store.Get(ctx, "fast")
	if err != nil {
		t.Fatalf("Get(fast) error = %v", err)
	}
	if !gotRedirect.IsRedirect() {
		t.Fatalf("Get(fast).IsRedirect() = false, want true")
	}
	if len(gotRedirect.Targets) != 1 || gotRedirect.Targets[0].Model != "gpt-4o" || gotRedirect.Targets[0].Provider != "openai" {
		t.Fatalf("Get(fast).Targets = %#v, want [{openai gpt-4o 0}]", gotRedirect.Targets)
	}

	gotPolicy, err := store.Get(ctx, "openai/gpt-4o")
	if err != nil {
		t.Fatalf("Get(policy) error = %v", err)
	}
	if gotPolicy.IsRedirect() {
		t.Fatalf("Get(policy).IsRedirect() = true, want false")
	}
	if len(gotPolicy.UserPaths) != 1 || gotPolicy.UserPaths[0] != "/team" {
		t.Fatalf("Get(policy).UserPaths = %#v, want [/team]", gotPolicy.UserPaths)
	}
}

func TestSQLiteStore_GetMissingAndDelete(t *testing.T) {
	t.Parallel()
	store := newSQLiteVMStore(t)
	ctx := context.Background()

	if _, err := store.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}

	if err := store.Upsert(ctx, VirtualModel{Source: "x", Targets: []Target{{Model: "m"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := store.Delete(ctx, "x"); err != nil {
		t.Fatalf("Delete(x) error = %v", err)
	}
	if _, err := store.Get(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStore_UpsertAllIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vms := []VirtualModel{
		{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true},
		{Source: "openai/gpt-4o", Enabled: false},
	}

	// Success: the whole batch is committed.
	store := newSQLiteVMStore(t)
	if err := store.UpsertAll(ctx, vms); err != nil {
		t.Fatalf("UpsertAll() error = %v", err)
	}
	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List()) = %d, want 2", len(got))
	}

	// Failure: a cancelled context aborts the transaction and writes nothing —
	// the table is left untouched rather than partially seeded.
	store2 := newSQLiteVMStore(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store2.UpsertAll(cancelled, vms); err == nil {
		t.Fatal("UpsertAll(cancelled ctx) error = nil, want error")
	}
	got2, err := store2.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got2) != 0 {
		t.Fatalf("len(List()) = %d after failed UpsertAll, want 0 (atomic)", len(got2))
	}
}
