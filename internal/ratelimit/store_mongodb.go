package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoDBStore struct {
	rules *mongo.Collection
}

func NewMongoDBStore(ctx context.Context, database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	store := &MongoDBStore{
		rules: database.Collection("rate_limits"),
	}
	if err := store.migratePreScopeDocuments(ctx); err != nil {
		return nil, err
	}
	_, err := store.rules.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "scope", Value: 1}, {Key: "subject", Value: 1}, {Key: "period_seconds", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return nil, fmt.Errorf("create rate limit indexes: %w", err)
	}
	return store, nil
}

// migratePreScopeDocuments rewrites rules stored before rule scopes existed
// (keyed by user_path only) into the scoped shape, and drops the old unique
// index so it cannot reject scoped documents.
func (s *MongoDBStore) migratePreScopeDocuments(ctx context.Context) error {
	_, err := s.rules.UpdateMany(ctx,
		bson.D{{Key: "subject", Value: bson.D{{Key: "$exists", Value: false}}}},
		mongo.Pipeline{
			bson.D{{Key: "$set", Value: bson.D{
				{Key: "scope", Value: string(ScopeUserPath)},
				{Key: "subject", Value: "$user_path"},
			}}},
			bson.D{{Key: "$unset", Value: "user_path"}},
		},
	)
	if err != nil {
		return fmt.Errorf("migrate rate limit rules to scoped schema: %w", err)
	}
	// Best-effort: the index may not exist on fresh databases.
	_ = s.rules.Indexes().DropOne(ctx, "user_path_1_period_seconds_1")
	return nil
}

func (s *MongoDBStore) ListRules(ctx context.Context) ([]Rule, error) {
	cursor, err := s.rules.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "user_path", Value: 1}, {Key: "period_seconds", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list rate limit rules: %w", err)
	}
	defer cursor.Close(ctx)

	rules := make([]Rule, 0)
	for cursor.Next(ctx) {
		var rule Rule
		if err := cursor.Decode(&rule); err != nil {
			return nil, fmt.Errorf("decode rate limit rule: %w", err)
		}
		rules = append(rules, rule)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate rate limit rules: %w", err)
	}
	return rules, nil
}

func (s *MongoDBStore) UpsertRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	return s.upsertNormalizedRules(ctx, rules)
}

func (s *MongoDBStore) upsertNormalizedRules(ctx context.Context, rules []Rule) error {
	if len(rules) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(rules))
	for _, rule := range rules {
		filter := bson.D{{Key: "scope", Value: rule.Scope}, {Key: "subject", Value: rule.Subject}, {Key: "period_seconds", Value: rule.PeriodSeconds}}
		// Mirror the SQL stores' source precedence: a config-sourced write may
		// only update rows that are themselves config-sourced. When a manual
		// row holds the key, the source-scoped filter misses it and the upsert
		// tries to insert a duplicate instead — the unique index rejects that,
		// and the duplicate-key error is treated as a benign skip below.
		if rule.Source == SourceConfig {
			filter = append(filter, bson.E{Key: "source", Value: SourceConfig})
		}
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "scope", Value: rule.Scope},
			{Key: "subject", Value: rule.Subject},
			{Key: "period_seconds", Value: rule.PeriodSeconds},
			{Key: "max_requests", Value: rule.MaxRequests},
			{Key: "max_tokens", Value: rule.MaxTokens},
			{Key: "source", Value: rule.Source},
			{Key: "updated_at", Value: rule.UpdatedAt},
		}}, {Key: "$setOnInsert", Value: bson.D{
			{Key: "created_at", Value: rule.CreatedAt},
		}}}
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(update).
			SetUpsert(true))
	}
	opts := options.BulkWrite().SetOrdered(false)
	_, err := s.rules.BulkWrite(ctx, models, opts)
	switch classifyBulkWriteError(err, rules) {
	case bulkWriteOK, bulkWriteShadowedByManual:
		return nil
	case bulkWriteRetryManualRace:
		// A manual write lost an insert race to a concurrent writer. The
		// documents exist now, so one retry applies the values as plain
		// updates (last write wins, matching the SQL stores).
		retryErr := func() error {
			_, err := s.rules.BulkWrite(ctx, models, opts)
			return err
		}()
		switch classifyBulkWriteError(retryErr, rules) {
		case bulkWriteOK, bulkWriteShadowedByManual:
			return nil
		default:
			return fmt.Errorf("upsert %d rate limit rules (after duplicate-key retry): %w", len(rules), retryErr)
		}
	default:
		return fmt.Errorf("upsert %d rate limit rules: %w", len(rules), err)
	}
}

// bulkWriteOutcome names how a bulk upsert error should be handled.
type bulkWriteOutcome int

const (
	bulkWriteOK bulkWriteOutcome = iota
	// bulkWriteShadowedByManual: every failed write is a duplicate key on a
	// config-sourced rule — manual rows shadowing config seeds, the intended
	// precedence.
	bulkWriteShadowedByManual
	// bulkWriteRetryManualRace: duplicate keys on manual rules — a lost
	// insert race worth one retry, which lands as a plain update.
	bulkWriteRetryManualRace
	// bulkWriteFailed: anything else is a real failure.
	bulkWriteFailed
)

func classifyBulkWriteError(err error, rules []Rule) bulkWriteOutcome {
	if err == nil {
		return bulkWriteOK
	}
	if duplicateKeyErrorsOnConfigRulesOnly(err, rules) {
		return bulkWriteShadowedByManual
	}
	if isOnlyDuplicateKeyErrors(err) {
		return bulkWriteRetryManualRace
	}
	return bulkWriteFailed
}

// duplicateKeyErrorsOnConfigRulesOnly reports whether every write error is a
// duplicate-key violation on a config-sourced rule. With the source-scoped
// config filter above, those are exactly the manual rows shadowing config
// seeds — the intended precedence, not a failure. Duplicate keys on manual
// rules are real insert races and must not be swallowed.
func duplicateKeyErrorsOnConfigRulesOnly(err error, rules []Rule) bool {
	var bulkErr mongo.BulkWriteException
	if !errors.As(err, &bulkErr) {
		return false
	}
	if bulkErr.WriteConcernError != nil || len(bulkErr.WriteErrors) == 0 {
		return false
	}
	for _, writeErr := range bulkErr.WriteErrors {
		if !writeErr.HasErrorCode(11000) {
			return false
		}
		if writeErr.Index < 0 || writeErr.Index >= len(rules) || rules[writeErr.Index].Source != SourceConfig {
			return false
		}
	}
	return true
}

// isOnlyDuplicateKeyErrors reports whether every write error in a bulk write
// failure is a duplicate-key violation.
func isOnlyDuplicateKeyErrors(err error) bool {
	var bulkErr mongo.BulkWriteException
	if !errors.As(err, &bulkErr) {
		return false
	}
	if bulkErr.WriteConcernError != nil || len(bulkErr.WriteErrors) == 0 {
		return false
	}
	for _, writeErr := range bulkErr.WriteErrors {
		if !writeErr.HasErrorCode(11000) {
			return false
		}
	}
	return true
}

func (s *MongoDBStore) DeleteRule(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	scope, subject, err := normalizeRuleKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	result, err := s.rules.DeleteOne(ctx, bson.D{{Key: "scope", Value: scope}, {Key: "subject", Value: subject}, {Key: "period_seconds", Value: periodSeconds}})
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
	}
	return nil
}

func (s *MongoDBStore) ReplaceConfigRules(ctx context.Context, rules []Rule) error {
	rules, err := normalizeRulesForUpsert(rules)
	if err != nil {
		return err
	}
	for i := range rules {
		rules[i].Source = SourceConfig
	}

	session, err := s.rules.Database().Client().StartSession()
	if err != nil {
		return fmt.Errorf("start config rate limit replacement transaction: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if err := s.replaceConfigRules(txCtx, rules); err != nil {
			if isMongoTransactionCapabilityError(err) {
				return nil, &mongoTransactionFallbackError{err: err}
			}
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		if fallbackErr := mongoTransactionFallbackCause(err); fallbackErr != nil || isMongoTransactionCapabilityError(err) {
			if fallbackErr == nil {
				fallbackErr = err
			}
			slog.Warn("MongoDB transactions unavailable for rate limit config replacement; falling back to non-transactional update", "error", fallbackErr)
			if err := s.replaceConfigRules(ctx, rules); err != nil {
				return fmt.Errorf("replace config rate limit rules without transaction: %w", errors.Join(fallbackErr, err))
			}
			return nil
		}
		return fmt.Errorf("replace config rate limit rules transaction: %w", err)
	}
	return nil
}

func (s *MongoDBStore) replaceConfigRules(ctx context.Context, rules []Rule) error {
	filter := bson.D{{Key: "source", Value: SourceConfig}}
	if len(rules) > 0 {
		keep := make(bson.A, 0, len(rules))
		for _, rule := range rules {
			keep = append(keep, bson.D{
				{Key: "scope", Value: rule.Scope},
				{Key: "subject", Value: rule.Subject},
				{Key: "period_seconds", Value: rule.PeriodSeconds},
			})
		}
		filter = append(filter, bson.E{Key: "$nor", Value: keep})
	}
	if _, err := s.rules.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete old config rate limit rules: %w", err)
	}
	configRules, err := s.configRulesWithoutManualCollisions(ctx, rules)
	if err != nil {
		return err
	}
	return s.upsertNormalizedRules(ctx, configRules)
}

// configRulesWithoutManualCollisions drops config rules whose key already has
// a manual row, so admin edits keep winning over config seeds.
func (s *MongoDBStore) configRulesWithoutManualCollisions(ctx context.Context, rules []Rule) ([]Rule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	keys := make(bson.A, 0, len(rules))
	for _, rule := range rules {
		keys = append(keys, bson.D{
			{Key: "scope", Value: rule.Scope},
			{Key: "subject", Value: rule.Subject},
			{Key: "period_seconds", Value: rule.PeriodSeconds},
		})
	}
	cursor, err := s.rules.Find(ctx, bson.D{{Key: "$or", Value: keys}})
	if err != nil {
		return nil, fmt.Errorf("find existing config rate limit collisions: %w", err)
	}
	defer cursor.Close(ctx)

	existingSources := make(map[string]string, len(rules))
	for cursor.Next(ctx) {
		var existing Rule
		if err := cursor.Decode(&existing); err != nil {
			return nil, fmt.Errorf("decode existing rate limit collision: %w", err)
		}
		existingSources[ruleStoreKey(existing.Scope, existing.Subject, existing.PeriodSeconds)] = existing.Source
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing rate limit collisions: %w", err)
	}

	filtered := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		if source, ok := existingSources[ruleStoreKey(rule.Scope, rule.Subject, rule.PeriodSeconds)]; ok && source != "" && source != SourceConfig {
			continue
		}
		filtered = append(filtered, rule)
	}
	return filtered, nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

type mongoTransactionFallbackError struct {
	err error
}

func (e *mongoTransactionFallbackError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func mongoTransactionFallbackCause(err error) error {
	var fallbackErr *mongoTransactionFallbackError
	if errors.As(err, &fallbackErr) {
		return fallbackErr.err
	}
	return nil
}

func isMongoTransactionCapabilityError(err error) bool {
	if err == nil {
		return false
	}
	var commandErr mongo.CommandError
	if errors.As(err, &commandErr) && commandErr.HasErrorCode(20) {
		return true
	}
	var labeled mongo.LabeledError
	if errors.As(err, &labeled) && labeled.HasErrorLabel("TransientTransactionError") {
		message := strings.ToLower(err.Error())
		return strings.Contains(message, "transaction") &&
			(strings.Contains(message, "not supported") ||
				strings.Contains(message, "not allowed") ||
				strings.Contains(message, "replica set"))
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "transaction numbers are only allowed on a replica set member or mongos")
}
