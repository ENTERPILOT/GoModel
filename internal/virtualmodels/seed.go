package virtualmodels

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/internal/storage"
)

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

	for _, vm := range toSeed {
		if err := store.Upsert(ctx, vm); err != nil {
			return fmt.Errorf("seed virtual model %q: %w", vm.Source, err)
		}
	}

	if len(toSeed) > 0 {
		slog.Info("virtualmodels: seeded virtual_models from legacy aliases and access overrides", "count", len(toSeed))
	}
	return nil
}
