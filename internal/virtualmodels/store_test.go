package virtualmodels

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func TestStore_RoundTripRedirectAndPolicy(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		redirect := VirtualModel{
			Source:      "fast",
			Targets:     []Target{{Provider: "openai", Model: "gpt-4o"}},
			Description: "primary",
			Slowdown:    new(0.4),
			Enabled:     true,
		}
		policy := VirtualModel{
			Source:       "openai/gpt-4o",
			ProviderName: "openai",
			Model:        "gpt-4o",
			UserPaths:    []string{"/team"},
			Slowdown:     new(0.2),
			Enabled:      true,
		}
		disabledOverride := VirtualModel{
			Source:   "no-slowdown",
			Targets:  []Target{{Provider: "openai", Model: "gpt-4o"}},
			Slowdown: new(0.0),
			Enabled:  true,
		}
		if err := store.Upsert(ctx, redirect); err != nil {
			t.Fatalf("Upsert(redirect) error = %v", err)
		}
		if err := store.Upsert(ctx, policy); err != nil {
			t.Fatalf("Upsert(policy) error = %v", err)
		}
		if err := store.Upsert(ctx, disabledOverride); err != nil {
			t.Fatalf("Upsert(disabled override) error = %v", err)
		}

		got, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len(List()) = %d, want 3", len(got))
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
		if gotRedirect.Slowdown == nil || *gotRedirect.Slowdown != 0.4 {
			t.Fatalf("Get(fast).Slowdown = %v, want 0.4", gotRedirect.Slowdown)
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
		if gotPolicy.Slowdown == nil || *gotPolicy.Slowdown != 0.2 {
			t.Fatalf("Get(policy).Slowdown = %v, want 0.2", gotPolicy.Slowdown)
		}

		gotDisabled, err := store.Get(ctx, "no-slowdown")
		if err != nil {
			t.Fatalf("Get(no-slowdown) error = %v", err)
		}
		if gotDisabled.Slowdown == nil || *gotDisabled.Slowdown != 0 {
			t.Fatalf("Get(no-slowdown).Slowdown = %v, want explicit zero", gotDisabled.Slowdown)
		}
	})
}

func TestStore_GetMissingAndDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
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
	})
}

func TestSQLStore_ListSurfacesUndecodableRow(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		if err := store.Upsert(ctx, VirtualModel{Source: "ok", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// A row whose targets column is not JSON cannot be scanned; List must
		// report it rather than return a partial list.
		if _, err := db.Exec(ctx, `INSERT INTO virtual_models (source, targets, created_at, updated_at) VALUES ('broken', 'not-json', 0, 0)`); err != nil {
			t.Fatalf("insert broken row: %v", err)
		}
		if _, err := store.List(ctx); err == nil || !strings.Contains(err.Error(), "decode targets") {
			t.Fatalf("List() error = %v, want decode targets failure", err)
		}
	})
}
