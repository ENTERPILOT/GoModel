package virtualmodels

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/goccy/go-json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// legacyFailoverTable is the store of the standalone failover feature that the
// failover load-balancing strategy replaced.
const legacyFailoverTable = "failover_rules"

// legacyFailoverRule is one row of that store.
type legacyFailoverRule struct {
	Source        string   `bson:"_id"`
	Fallbacks     []string `bson:"fallback_models"`
	Enabled       bool     `bson:"enabled"`
	ManagedSource string   `bson:"managed_source"`
}

// importLegacyFailoverRules converts the dashboard-managed rows of the legacy
// failover_rules store into failover-strategy virtual models and then drops
// the store, so the conversion runs once. A rule whose primary model already
// has a virtual model is not merged — the operator decides how the two should
// combine — and keeps the store in place, so its fallback list stays readable
// and the warning repeats on every start until it is resolved. Config-managed
// rows are skipped — the live configuration still declares them and
// FailoverConfigModels translates it on every start. Databases that never had
// the store are a no-op.
func importLegacyFailoverRules(ctx context.Context, store Store, conn storage.Storage) error {
	rules, err := readLegacyFailoverRules(ctx, conn)
	if err != nil {
		return fmt.Errorf("read legacy failover rules: %w", err)
	}
	if rules == nil {
		return nil
	}
	existing, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list virtual models: %w", err)
	}
	taken := make(map[string]VirtualModel, len(existing))
	for _, row := range existing {
		taken[row.Source] = row
	}

	migrated, unresolved := 0, 0
	for _, rule := range rules {
		model, convertible := failoverModel(rule.Source, rule.Fallbacks, false)
		if rule.Enabled && rule.ManagedSource != "config" && convertible {
			// A previous start that stopped between the upsert and the row
			// delete left its own conversion behind; finish it, do not
			// report it as a collision.
			if existing, exists := taken[rule.Source]; exists && !isMigratedFailoverModel(existing) {
				slog.Warn("legacy failover rule not migrated: a virtual model with the same source exists; add its fallbacks as targets with the failover strategy, then delete the failover_rules row",
					"source", rule.Source, "fallbacks", rule.Fallbacks)
				unresolved++
				continue
			} else if !exists {
				if err := store.Upsert(ctx, model); err != nil {
					return fmt.Errorf("migrate legacy failover rule %q: %w", rule.Source, err)
				}
				taken[rule.Source] = model
				migrated++
			}
		}
		// Converted and obsolete rows leave the store, so only the rows that
		// still need the operator remain — and a later start does not mistake
		// a converted row for a collision.
		if err := deleteLegacyFailoverRule(ctx, conn, rule.Source); err != nil {
			return fmt.Errorf("remove legacy failover rule %q: %w", rule.Source, err)
		}
	}
	if unresolved > 0 {
		slog.Warn("legacy failover_rules store kept until every remaining rule is resolved", "unresolved", unresolved)
		return nil
	}
	if err := dropLegacyFailoverStore(ctx, conn); err != nil {
		return fmt.Errorf("drop legacy failover rules: %w", err)
	}
	slog.Info("migrated legacy failover rules into failover-strategy virtual models",
		"rules", len(rules), "migrated", migrated)
	return nil
}

// readLegacyFailoverRules lists the legacy store's rows, or returns nil when
// the store does not exist.
func readLegacyFailoverRules(ctx context.Context, conn storage.Storage) ([]legacyFailoverRule, error) {
	return storage.ResolveSQLBackend[[]legacyFailoverRule](ctx, conn,
		func(db sqlx.DB) ([]legacyFailoverRule, error) {
			rows, err := db.Query(ctx, "SELECT primary_model, fallback_models, enabled, managed_source FROM "+legacyFailoverTable)
			if err != nil {
				if isMissingTableError(err) {
					return nil, nil
				}
				return nil, err
			}
			defer rows.Close()
			rules := []legacyFailoverRule{}
			for rows.Next() {
				var rule legacyFailoverRule
				var fallbacks []byte
				if err := rows.Scan(&rule.Source, &fallbacks, &rule.Enabled, &rule.ManagedSource); err != nil {
					return nil, err
				}
				if len(fallbacks) > 0 {
					if err := json.Unmarshal(fallbacks, &rule.Fallbacks); err != nil {
						return nil, fmt.Errorf("decode fallbacks of %q: %w", rule.Source, err)
					}
				}
				rules = append(rules, rule)
			}
			return rules, rows.Err()
		},
		func(db *mongo.Database) ([]legacyFailoverRule, error) {
			names, err := db.ListCollectionNames(ctx, bson.M{"name": legacyFailoverTable})
			if err != nil {
				return nil, err
			}
			if len(names) == 0 {
				return nil, nil
			}
			cursor, err := db.Collection(legacyFailoverTable).Find(ctx, bson.M{})
			if err != nil {
				return nil, err
			}
			rules := []legacyFailoverRule{}
			if err := cursor.All(ctx, &rules); err != nil {
				return nil, err
			}
			return rules, nil
		})
}

// isMigratedFailoverModel reports whether vm is a conversion this migration
// wrote, by the provenance failoverModel stamps on it.
func isMigratedFailoverModel(vm VirtualModel) bool {
	return normalizeStrategy(vm.Strategy) == StrategyFailover && vm.Description == migratedFailoverDescription
}

func deleteLegacyFailoverRule(ctx context.Context, conn storage.Storage, source string) error {
	_, err := storage.ResolveSQLBackend[struct{}](ctx, conn,
		func(db sqlx.DB) (struct{}, error) {
			_, err := db.Exec(ctx, "DELETE FROM "+legacyFailoverTable+" WHERE primary_model = ?", source)
			return struct{}{}, err
		},
		func(db *mongo.Database) (struct{}, error) {
			_, err := db.Collection(legacyFailoverTable).DeleteOne(ctx, bson.M{"_id": source})
			return struct{}{}, err
		})
	return err
}

func dropLegacyFailoverStore(ctx context.Context, conn storage.Storage) error {
	_, err := storage.ResolveSQLBackend[struct{}](ctx, conn,
		func(db sqlx.DB) (struct{}, error) {
			_, err := db.Exec(ctx, "DROP TABLE "+legacyFailoverTable)
			return struct{}{}, err
		},
		func(db *mongo.Database) (struct{}, error) {
			return struct{}{}, db.Collection(legacyFailoverTable).Drop(ctx)
		})
	return err
}
