package virtualmodels

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gomodel/internal/aliases"
	"gomodel/internal/modeloverrides"
	"gomodel/internal/storage"
)

// seedFromLegacy performs a one-time, idempotent copy of legacy `aliases` and
// `model_overrides` rows into `virtual_models` when the latter is still empty.
//
// REMOVE-LATER (cleanup milestone: one release after virtual models ship).
// Once all environments run the unified store, delete this file, the legacy
// aliases/modeloverrides packages, and their tables/collections.
func seedFromLegacy(ctx context.Context, store Store, conn storage.Storage) error {
	existing, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list virtual models: %w", err)
	}
	if len(existing) > 0 {
		// Already populated (seeded or operator-managed). Nothing to do.
		return nil
	}

	legacyAliases, err := storage.ResolveBackend[aliases.Store](
		conn,
		func(db *sql.DB) (aliases.Store, error) { return aliases.NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (aliases.Store, error) { return aliases.NewPostgreSQLStore(ctx, pool) },
		func(db *mongo.Database) (aliases.Store, error) { return aliases.NewMongoDBStore(db) },
	)
	if err != nil {
		return fmt.Errorf("open legacy aliases store: %w", err)
	}
	legacyOverrides, err := storage.ResolveBackend[modeloverrides.Store](
		conn,
		func(db *sql.DB) (modeloverrides.Store, error) { return modeloverrides.NewSQLiteStore(db) },
		func(pool *pgxpool.Pool) (modeloverrides.Store, error) {
			return modeloverrides.NewPostgreSQLStore(ctx, pool)
		},
		func(db *mongo.Database) (modeloverrides.Store, error) { return modeloverrides.NewMongoDBStore(db) },
	)
	if err != nil {
		return fmt.Errorf("open legacy model overrides store: %w", err)
	}

	seen := make(map[string]struct{})

	legacyAliasRows, err := legacyAliases.List(ctx)
	if err != nil {
		return fmt.Errorf("list legacy aliases: %w", err)
	}
	for _, alias := range legacyAliasRows {
		vm := vmFromAlias(alias)
		if err := store.Upsert(ctx, vm); err != nil {
			return fmt.Errorf("seed alias %q: %w", vm.Source, err)
		}
		seen[vm.Source] = struct{}{}
	}

	legacyOverrideRows, err := legacyOverrides.List(ctx)
	if err != nil {
		return fmt.Errorf("list legacy model overrides: %w", err)
	}
	for _, override := range legacyOverrideRows {
		vm := vmFromOverride(override)
		if _, taken := seen[vm.Source]; taken {
			// Source-namespace collision: an alias and an access override share
			// the same name. We must not silently drop the override — that would
			// remove an access control and could expose a model that was gated.
			// Fail closed and ask the operator to rename the alias or the
			// override selector before upgrading.
			return fmt.Errorf(
				"virtual models migration conflict: source %q is used by both an alias and an access override; "+
					"rename the alias or remove/rename the access override (selector %q) before upgrading",
				vm.Source, override.Selector)
		}
		if err := store.Upsert(ctx, vm); err != nil {
			return fmt.Errorf("seed access override %q: %w", vm.Source, err)
		}
		seen[vm.Source] = struct{}{}
	}

	if len(seen) > 0 {
		slog.Info("virtualmodels: seeded virtual_models from legacy aliases and access overrides", "count", len(seen))
	}
	return nil
}
