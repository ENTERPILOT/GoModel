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
	_, err := store.rules.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "user_path", Value: 1}, {Key: "period_seconds", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return nil, fmt.Errorf("create rate limit indexes: %w", err)
	}
	return store, nil
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
		filter := bson.D{{Key: "user_path", Value: rule.UserPath}, {Key: "period_seconds", Value: rule.PeriodSeconds}}
		// Mirror the SQL stores' source precedence: a config-sourced write may
		// only update rows that are themselves config-sourced. When a manual
		// row holds the key, the source-scoped filter misses it and the upsert
		// tries to insert a duplicate instead — the unique index rejects that,
		// and the duplicate-key error is treated as a benign skip below.
		if rule.Source == SourceConfig {
			filter = append(filter, bson.E{Key: "source", Value: SourceConfig})
		}
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "user_path", Value: rule.UserPath},
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
	if _, err := s.rules.BulkWrite(ctx, models, opts); err != nil {
		if isOnlyDuplicateKeyErrors(err) {
			return nil
		}
		return fmt.Errorf("upsert %d rate limit rules: %w", len(rules), err)
	}
	return nil
}

// isOnlyDuplicateKeyErrors reports whether every write error in a bulk write
// failure is a duplicate-key violation. With the source-scoped config filter
// above, those are exactly the manual rows shadowing config seeds — the
// intended precedence, not a failure.
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

func (s *MongoDBStore) DeleteRule(ctx context.Context, userPath string, periodSeconds int64) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if err := validatePeriodSeconds(periodSeconds); err != nil {
		return err
	}
	result, err := s.rules.DeleteOne(ctx, bson.D{{Key: "user_path", Value: userPath}, {Key: "period_seconds", Value: periodSeconds}})
	if err != nil {
		return fmt.Errorf("delete rate limit rule %s/%d: %w", userPath, periodSeconds, err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
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
				{Key: "user_path", Value: rule.UserPath},
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
			{Key: "user_path", Value: rule.UserPath},
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
		existingSources[ruleStoreKey(existing.UserPath, existing.PeriodSeconds)] = existing.Source
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing rate limit collisions: %w", err)
	}

	filtered := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		if source, ok := existingSources[ruleStoreKey(rule.UserPath, rule.PeriodSeconds)]; ok && source != "" && source != SourceConfig {
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
