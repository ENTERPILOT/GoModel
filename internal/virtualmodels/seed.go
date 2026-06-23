package virtualmodels

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/internal/storage"
)

// transactionalSeeder is an optional Store capability: an atomic batch write so
// the legacy seed is all-or-nothing. The SQL stores implement it; backends
// without transactions fall back to per-row writes with best-effort rollback.
type transactionalSeeder interface {
	UpsertAll(ctx context.Context, vms []VirtualModel) error
}

// seedFromLegacy performs a one-time, idempotent copy of legacy `aliases` and
// `model_overrides` rows into `virtual_models` when the latter is still empty.
//
// REMOVE-LATER (cleanup milestone: one release after virtual models ship).
// Once all environments run the unified store, delete this file, seed_legacy.go,
// and the legacy aliases/model_overrides tables/collections.
func seedFromLegacy(ctx context.Context, store Store, conn storage.Storage) error {
	existing, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list virtual models: %w", err)
	}
	if len(existing) > 0 {
		// Already populated (seeded or operator-managed). Nothing to do.
		return nil
	}

	legacyAliasRows, err := storage.ResolveBackend[[]legacyAlias](
		conn,
		func(db *sql.DB) ([]legacyAlias, error) { return readLegacyAliasesSQLite(ctx, db) },
		func(pool *pgxpool.Pool) ([]legacyAlias, error) { return readLegacyAliasesPostgreSQL(ctx, pool) },
		func(db *mongo.Database) ([]legacyAlias, error) { return readLegacyAliasesMongo(ctx, db) },
	)
	if err != nil {
		return fmt.Errorf("read legacy aliases: %w", err)
	}
	legacyOverrideRows, err := storage.ResolveBackend[[]legacyOverride](
		conn,
		func(db *sql.DB) ([]legacyOverride, error) { return readLegacyOverridesSQLite(ctx, db) },
		func(pool *pgxpool.Pool) ([]legacyOverride, error) { return readLegacyOverridesPostgreSQL(ctx, pool) },
		func(db *mongo.Database) ([]legacyOverride, error) { return readLegacyOverridesMongo(ctx, db) },
	)
	if err != nil {
		return fmt.Errorf("read legacy model overrides: %w", err)
	}

	// Resolve every row and detect source-namespace collisions BEFORE writing
	// anything. If a collision aborted the seed mid-write, the partially seeded
	// table would trip the len(existing) > 0 guard on the next startup and the
	// access overrides would never be imported, leaving redirects without their
	// access controls. Building the full set first makes the seed all-or-nothing
	// with respect to collisions.
	seen := make(map[string]struct{}, len(legacyAliasRows)+len(legacyOverrideRows))
	toSeed := make([]VirtualModel, 0, len(legacyAliasRows)+len(legacyOverrideRows))

	for _, alias := range legacyAliasRows {
		vm := alias.toRedirect()
		seen[vm.Source] = struct{}{}
		toSeed = append(toSeed, vm)
	}

	for _, override := range legacyOverrideRows {
		vm := override.toPolicy()
		if _, taken := seen[vm.Source]; taken {
			// Source-namespace collision: an alias and an access override share
			// the same name. We must not silently drop the override — that would
			// remove an access control and could expose a model that was gated.
			// Fail closed (before any write) and ask the operator to rename the
			// alias or the override selector before upgrading.
			return fmt.Errorf(
				"virtual models migration conflict: source %q is used by both an alias and an access override; "+
					"rename the alias or remove/rename the access override (selector %q) before upgrading",
				vm.Source, override.Selector)
		}
		seen[vm.Source] = struct{}{}
		toSeed = append(toSeed, vm)
	}

	// Prefer an atomic batch write so a failed seed leaves the table untouched
	// rather than partially populated — a partial seed would otherwise trip the
	// len(existing) > 0 guard on the next start and skip importing the rest,
	// leaving previously restricted models without their access controls. Backends
	// without transactions (e.g. MongoDB) fall back to per-row writes with
	// best-effort rollback.
	if seeder, ok := store.(transactionalSeeder); ok {
		if err := seeder.UpsertAll(ctx, toSeed); err != nil {
			return fmt.Errorf("seed virtual models: %w", err)
		}
	} else {
		written := make([]string, 0, len(toSeed))
		for _, vm := range toSeed {
			if err := store.Upsert(ctx, vm); err != nil {
				rollbackPartialSeed(store, written)
				return fmt.Errorf("seed virtual model %q: %w", vm.Source, err)
			}
			written = append(written, vm.Source)
		}
	}

	if len(toSeed) > 0 {
		slog.Info("virtualmodels: seeded virtual_models from legacy aliases and access overrides", "count", len(toSeed))
	}
	return nil
}

// rollbackPartialSeed best-effort deletes rows already written by a failed seed,
// using a fresh context since the seed's context may itself have been cancelled.
func rollbackPartialSeed(store Store, sources []string) {
	if len(sources) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, source := range sources {
		if err := store.Delete(ctx, source); err != nil {
			slog.Error("virtualmodels: failed to roll back partial seed", "source", source, "error", err)
		}
	}
}
