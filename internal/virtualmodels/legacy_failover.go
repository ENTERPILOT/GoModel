package virtualmodels

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/goccy/go-json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// legacyFailoverTable is the store of the standalone failover feature that the
// failover load-balancing strategy replaced.
const legacyFailoverTable = "failover_rules"

// legacyFailoverRule is one row of that store. The store went through a
// rename (source → primary_model, targets → fallback_models) that its own
// constructor used to apply at startup; that constructor is gone, so both
// shapes are read here and the older fields take over when the newer ones
// are absent.
type legacyFailoverRule struct {
	Source        string   `bson:"_id"`
	Fallbacks     []string `bson:"fallback_models"`
	Targets       []string `bson:"targets"`
	Enabled       *bool    `bson:"enabled"`
	ManagedSource string   `bson:"managed_source"`
}

// fallbacks returns the rule's fallback list from whichever field holds it.
func (r legacyFailoverRule) fallbacks() []string {
	if r.Fallbacks != nil {
		return r.Fallbacks
	}
	return r.Targets
}

// enabled reports the rule's enabled flag; a row written before the flag
// existed is enabled.
func (r legacyFailoverRule) enabled() bool {
	return r.Enabled == nil || *r.Enabled
}

// importLegacyFailoverRules converts the dashboard-managed rows of the legacy
// failover_rules store into failover-strategy virtual models and then drops
// the store, so the conversion runs once. A rule whose primary model already
// has a virtual model is merged only when that row is a plain per-model
// policy whose settings keep their meaning on a redirect (see
// mergeableFailoverPolicy); otherwise the operator decides how the two should
// combine, and the store is kept in place so the fallback list stays readable
// and the warning repeats on every start until it is resolved. Config-managed
// rows are skipped — the live configuration still declares them and
// FailoverConfigModels translates it on every start. Databases that never had
// the store are a no-op.
func importLegacyFailoverRules(ctx context.Context, store Store, conn storage.Storage) error {
	rules, keyColumn, err := readLegacyFailoverRules(ctx, conn)
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
		model, convertible := failoverModel(rule.Source, rule.fallbacks(), false)
		if rule.enabled() && rule.ManagedSource != "config" && convertible {
			existing, exists := taken[rule.Source]
			switch {
			case !exists:
				if err := store.Upsert(ctx, model); err != nil {
					return fmt.Errorf("migrate legacy failover rule %q: %w", rule.Source, err)
				}
				taken[rule.Source] = model
				migrated++
			case isMigratedFailoverModel(existing, model):
				// A previous start that stopped between the upsert and the
				// row delete left its own conversion behind; finish it.
			case mergeableFailoverPolicy(existing):
				merged := mergeFailoverPolicy(existing, model)
				if err := store.Upsert(ctx, merged); err != nil {
					return fmt.Errorf("migrate legacy failover rule %q into its policy: %w", rule.Source, err)
				}
				taken[rule.Source] = merged
				migrated++
			default:
				slog.Warn("legacy failover rule not migrated: a virtual model with the same source exists; add its fallbacks as targets with the failover strategy, then delete the failover_rules row",
					"source", rule.Source, "fallbacks", rule.fallbacks())
				unresolved++
				continue
			}
		}
		// Converted and obsolete rows leave the store, so only the rows that
		// still need the operator remain — and a later start does not mistake
		// a converted row for a collision.
		if err := deleteLegacyFailoverRule(ctx, conn, keyColumn, rule.Source); err != nil {
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

// legacyFailoverRows is the legacy store's content plus the name of its SQL
// key column, which the row deletes need since the store had two shapes.
type legacyFailoverRows struct {
	rules     []legacyFailoverRule
	keyColumn string
}

// readLegacyFailoverRules lists the legacy store's rows, or returns nil rules
// when the store does not exist.
func readLegacyFailoverRules(ctx context.Context, conn storage.Storage) ([]legacyFailoverRule, string, error) {
	read, err := storage.ResolveSQLBackend[legacyFailoverRows](ctx, conn,
		func(db sqlx.DB) (legacyFailoverRows, error) {
			rules, keyColumn, err := readLegacySQLFailoverRules(ctx, db)
			return legacyFailoverRows{rules: rules, keyColumn: keyColumn}, err
		},
		func(db *mongo.Database) (legacyFailoverRows, error) {
			rules, err := readLegacyMongoFailoverRules(ctx, db)
			return legacyFailoverRows{rules: rules}, err
		})
	return read.rules, read.keyColumn, err
}

func readLegacySQLFailoverRules(ctx context.Context, db sqlx.DB) ([]legacyFailoverRule, string, error) {
	columns, err := legacyFailoverColumns(ctx, db)
	if err != nil || columns == nil {
		return nil, "", err
	}
	// Read each value from whichever column this database still has,
	// defaulting the ones a very old table never had.
	pick := func(preferred, legacy, missing string) string {
		switch {
		case columns[preferred]:
			return preferred
		case columns[legacy]:
			return legacy
		}
		return missing
	}
	keyColumn := pick("primary_model", "source", "")
	if keyColumn == "" {
		return nil, "", fmt.Errorf("%s has neither a primary_model nor a source column", legacyFailoverTable)
	}
	query := fmt.Sprintf("SELECT TRIM(%s), %s, %s, %s FROM %s",
		keyColumn,
		pick("fallback_models", "targets", "'[]'"),
		pick("enabled", "", "1"),
		pick("managed_source", "", "'dashboard'"),
		legacyFailoverTable)
	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	rules := []legacyFailoverRule{}
	for rows.Next() {
		var rule legacyFailoverRule
		var fallbacks []byte
		var enabled bool
		if err := rows.Scan(&rule.Source, &fallbacks, &enabled, &rule.ManagedSource); err != nil {
			return nil, "", err
		}
		rule.Enabled = &enabled
		if len(fallbacks) > 0 {
			if err := json.Unmarshal(fallbacks, &rule.Fallbacks); err != nil {
				return nil, "", fmt.Errorf("decode fallbacks of %q: %w", rule.Source, err)
			}
		}
		if rule.Source == "" {
			continue
		}
		rules = append(rules, rule)
	}
	return rules, keyColumn, rows.Err()
}

func readLegacyMongoFailoverRules(ctx context.Context, db *mongo.Database) ([]legacyFailoverRule, error) {
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
	for i := range rules {
		rules[i].Source = strings.TrimSpace(rules[i].Source)
	}
	return rules, nil
}

// legacyFailoverColumns lists the legacy table's column names, or nil when
// the table does not exist.
func legacyFailoverColumns(ctx context.Context, db sqlx.DB) (map[string]bool, error) {
	var query string
	switch db.Dialect() {
	case sqlx.PostgreSQL:
		query = "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '" + legacyFailoverTable + "'"
	default:
		query = "SELECT name FROM pragma_table_info('" + legacyFailoverTable + "')"
	}
	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", legacyFailoverTable, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, nil
	}
	return columns, nil
}

// mergeableFailoverPolicy reports whether a stored row can absorb a legacy
// failover rule without changing what it expresses: a per-model policy (no
// targets) that is enabled and not path-scoped. On a redirect, user_paths
// selects who is redirected rather than who may use the model, and a disabled
// redirect falls through to the model it would have disabled — so rows using
// either keep needing the operator.
func mergeableFailoverPolicy(vm VirtualModel) bool {
	return !vm.IsRedirect() && vm.Enabled && len(vm.UserPaths) == 0
}

// mergeFailoverPolicy turns a mergeable policy into the failover redirect its
// legacy rule expressed, keeping the policy's description and slowdown. The
// provenance marker is used only when the policy had no description, so a
// start interrupted between this upsert and the row delete reports the row
// as a collision for the operator rather than converting it twice.
func mergeFailoverPolicy(policy, model VirtualModel) VirtualModel {
	merged := model
	merged.Slowdown = policy.Slowdown
	if policy.Description != "" {
		merged.Description = policy.Description
	}
	return merged
}

// isMigratedFailoverModel reports whether vm is the conversion this migration
// would write for a rule: it carries the provenance failoverModel stamps and
// exactly the expected targets. Anything else is the operator's own model.
func isMigratedFailoverModel(vm, expected VirtualModel) bool {
	if normalizeStrategy(vm.Strategy) != StrategyFailover || vm.Description != migratedFailoverDescription || len(vm.Targets) != len(expected.Targets) {
		return false
	}
	for i, target := range vm.Targets {
		if target != expected.Targets[i] {
			return false
		}
	}
	return true
}

// deleteLegacyFailoverRule removes one row by its trimmed key; keyColumn is
// the SQL key column readLegacyFailoverRules found (unused for MongoDB).
func deleteLegacyFailoverRule(ctx context.Context, conn storage.Storage, keyColumn, source string) error {
	_, err := storage.ResolveSQLBackend[struct{}](ctx, conn,
		func(db sqlx.DB) (struct{}, error) {
			_, err := db.Exec(ctx, "DELETE FROM "+legacyFailoverTable+" WHERE TRIM("+keyColumn+") = ?", source)
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
