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
// has a virtual model is skipped with a warning rather than merged: the
// operator decides how the two should combine. Config-managed rows are
// skipped too — the live configuration still declares them and
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
	taken := make(map[string]struct{}, len(existing))
	for _, row := range existing {
		taken[row.Source] = struct{}{}
	}

	migrated := 0
	for _, rule := range rules {
		if !rule.Enabled || len(rule.Fallbacks) == 0 || rule.ManagedSource == "config" {
			continue
		}
		if _, ok := taken[rule.Source]; ok {
			slog.Warn("legacy failover rule not migrated: a virtual model with the same source exists; add its fallbacks as targets with the failover strategy",
				"source", rule.Source, "fallbacks", rule.Fallbacks)
			continue
		}
		if err := store.Upsert(ctx, failoverModel(rule.Source, rule.Fallbacks, false)); err != nil {
			return fmt.Errorf("migrate legacy failover rule %q: %w", rule.Source, err)
		}
		migrated++
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
