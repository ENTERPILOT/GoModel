package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoDBStore struct {
	budgets  *mongo.Collection
	settings *mongo.Collection
	usage    *mongo.Collection
}

func NewMongoDBStore(ctx context.Context, database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	store := &MongoDBStore{
		budgets:  database.Collection("budgets"),
		settings: database.Collection("budget_settings"),
		usage:    database.Collection("usage"),
	}
	_, err := store.budgets.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "user_path", Value: 1}, {Key: "period_seconds", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "period_seconds", Value: 1}}},
	})
	if err != nil {
		return nil, fmt.Errorf("create budget indexes: %w", err)
	}
	_, err = store.settings.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "key", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return nil, fmt.Errorf("create budget settings indexes: %w", err)
	}
	return store, nil
}

func (s *MongoDBStore) ListBudgets(ctx context.Context) ([]Budget, error) {
	cursor, err := s.budgets.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "user_path", Value: 1}, {Key: "period_seconds", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	defer cursor.Close(ctx)

	budgets := make([]Budget, 0)
	for cursor.Next(ctx) {
		var budget Budget
		if err := cursor.Decode(&budget); err != nil {
			return nil, fmt.Errorf("decode budget: %w", err)
		}
		budgets = append(budgets, budget)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate budgets: %w", err)
	}
	return budgets, nil
}

func (s *MongoDBStore) UpsertBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	return s.upsertNormalizedBudgets(ctx, budgets)
}

func (s *MongoDBStore) upsertNormalizedBudgets(ctx context.Context, budgets []Budget) error {
	if len(budgets) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(budgets))
	for _, budget := range budgets {
		filter := bson.D{{Key: "user_path", Value: budget.UserPath}, {Key: "period_seconds", Value: budget.PeriodSeconds}}
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "user_path", Value: budget.UserPath},
			{Key: "period_seconds", Value: budget.PeriodSeconds},
			{Key: "amount", Value: budget.Amount},
			{Key: "source", Value: budget.Source},
			{Key: "updated_at", Value: budget.UpdatedAt},
		}}, {Key: "$setOnInsert", Value: bson.D{
			{Key: "created_at", Value: budget.CreatedAt},
			{Key: "last_reset_at", Value: budget.LastResetAt},
		}}}
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(update).
			SetUpsert(true))
	}
	if _, err := s.budgets.BulkWrite(ctx, models); err != nil {
		return fmt.Errorf("upsert %d budgets: %w", len(budgets), err)
	}
	return nil
}

func (s *MongoDBStore) DeleteBudget(ctx context.Context, userPath string, periodSeconds int64) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if periodSeconds <= 0 {
		return fmt.Errorf("period_seconds must be greater than 0")
	}
	result, err := s.budgets.DeleteOne(ctx, bson.D{{Key: "user_path", Value: userPath}, {Key: "period_seconds", Value: periodSeconds}})
	if err != nil {
		return fmt.Errorf("delete budget %s/%d: %w", userPath, periodSeconds, err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
	}
	return nil
}

func (s *MongoDBStore) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	for i := range budgets {
		budgets[i].Source = SourceConfig
	}

	session, err := s.budgets.Database().Client().StartSession()
	if err != nil {
		return fmt.Errorf("start config budget replacement transaction: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if err := s.replaceConfigBudgets(txCtx, budgets); err != nil {
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
			slog.Warn("MongoDB transactions unavailable for budget config replacement; falling back to non-transactional update", "error", fallbackErr)
			if err := s.replaceConfigBudgets(ctx, budgets); err != nil {
				return fmt.Errorf("replace config budgets without transaction: %w", errors.Join(fallbackErr, err))
			}
			return nil
		}
		return fmt.Errorf("replace config budgets transaction: %w", err)
	}
	return nil
}

func (s *MongoDBStore) replaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	filter := bson.D{{Key: "source", Value: SourceConfig}}
	if len(budgets) > 0 {
		keep := make(bson.A, 0, len(budgets))
		for _, budget := range budgets {
			keep = append(keep, bson.D{
				{Key: "user_path", Value: budget.UserPath},
				{Key: "period_seconds", Value: budget.PeriodSeconds},
			})
		}
		filter = append(filter, bson.E{Key: "$nor", Value: keep})
	}
	if _, err := s.budgets.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete old config budgets: %w", err)
	}
	configBudgets, err := s.configBudgetsWithoutManualCollisions(ctx, budgets)
	if err != nil {
		return err
	}
	return s.upsertNormalizedBudgets(ctx, configBudgets)
}

func (s *MongoDBStore) configBudgetsWithoutManualCollisions(ctx context.Context, budgets []Budget) ([]Budget, error) {
	if len(budgets) == 0 {
		return nil, nil
	}
	keys := make(bson.A, 0, len(budgets))
	for _, budget := range budgets {
		keys = append(keys, bson.D{
			{Key: "user_path", Value: budget.UserPath},
			{Key: "period_seconds", Value: budget.PeriodSeconds},
		})
	}
	cursor, err := s.budgets.Find(ctx, bson.D{{Key: "$or", Value: keys}})
	if err != nil {
		return nil, fmt.Errorf("find existing config budget collisions: %w", err)
	}
	defer cursor.Close(ctx)

	existingSources := make(map[string]string, len(budgets))
	for cursor.Next(ctx) {
		var existing Budget
		if err := cursor.Decode(&existing); err != nil {
			return nil, fmt.Errorf("decode existing budget collision: %w", err)
		}
		existingSources[budgetKey(existing.UserPath, existing.PeriodSeconds)] = existing.Source
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing budget collisions: %w", err)
	}

	filtered := make([]Budget, 0, len(budgets))
	for _, budget := range budgets {
		if source, ok := existingSources[budgetKey(budget.UserPath, budget.PeriodSeconds)]; ok && source != "" && source != SourceConfig {
			continue
		}
		filtered = append(filtered, budget)
	}
	return filtered, nil
}

func (s *MongoDBStore) GetSettings(ctx context.Context) (Settings, error) {
	cursor, err := s.settings.Find(ctx, bson.D{})
	if err != nil {
		return Settings{}, fmt.Errorf("get budget settings: %w", err)
	}
	defer cursor.Close(ctx)

	settings := DefaultSettings()
	var latest time.Time
	for cursor.Next(ctx) {
		var row struct {
			Key       string    `bson:"key"`
			Value     string    `bson:"value"`
			UpdatedAt time.Time `bson:"updated_at"`
		}
		if err := cursor.Decode(&row); err != nil {
			return Settings{}, fmt.Errorf("decode budget setting: %w", err)
		}
		if err := applySettingValue(&settings, row.Key, row.Value); err != nil {
			return Settings{}, err
		}
		if row.UpdatedAt.After(latest) {
			latest = row.UpdatedAt
		}
	}
	if err := cursor.Err(); err != nil {
		return Settings{}, fmt.Errorf("iterate budget settings: %w", err)
	}
	if !latest.IsZero() {
		settings.UpdatedAt = latest.UTC()
	}
	return normalizeLoadedSettings(settings)
}

func (s *MongoDBStore) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	settings.UpdatedAt = time.Now().UTC()

	session, err := s.settings.Database().Client().StartSession()
	if err != nil {
		return Settings{}, fmt.Errorf("start budget settings transaction: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if err := s.saveSettingsValues(txCtx, settings); err != nil {
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
			slog.Warn("MongoDB transactions unavailable for budget settings save; falling back to non-transactional update", "error", fallbackErr)
			if err := s.saveSettingsValues(ctx, settings); err != nil {
				return Settings{}, fmt.Errorf("save budget settings without transaction: %w", errors.Join(fallbackErr, err))
			}
			return settings, nil
		}
		return Settings{}, fmt.Errorf("save budget settings transaction: %w", err)
	}
	return settings, nil
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

func (s *MongoDBStore) saveSettingsValues(ctx context.Context, settings Settings) error {
	for key, value := range settingsKeyValues(settings) {
		filter := bson.D{{Key: "key", Value: key}}
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "key", Value: key},
			{Key: "value", Value: strconv.Itoa(value)},
			{Key: "updated_at", Value: settings.UpdatedAt},
		}}}
		if _, err := s.settings.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
			return fmt.Errorf("save budget setting %s: %w", key, err)
		}
	}
	return nil
}

func (s *MongoDBStore) ResetBudget(ctx context.Context, userPath string, periodSeconds int64, at time.Time) error {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return err
	}
	if periodSeconds <= 0 {
		return fmt.Errorf("period_seconds must be greater than 0")
	}
	result, err := s.budgets.UpdateOne(ctx,
		bson.D{{Key: "user_path", Value: userPath}, {Key: "period_seconds", Value: periodSeconds}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "last_reset_at", Value: at.UTC()},
			{Key: "updated_at", Value: at.UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("reset budget %s/%d: %w", userPath, periodSeconds, err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("%w: %s/%d", ErrNotFound, userPath, periodSeconds)
	}
	return nil
}

func (s *MongoDBStore) ResetAllBudgets(ctx context.Context, at time.Time) error {
	_, err := s.budgets.UpdateMany(ctx, bson.D{}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "last_reset_at", Value: at.UTC()},
		{Key: "updated_at", Value: at.UTC()},
	}}})
	if err != nil {
		return fmt.Errorf("reset all budgets: %w", err)
	}
	return nil
}

func (s *MongoDBStore) SumUsageCost(ctx context.Context, userPath string, start, end time.Time) (float64, bool, error) {
	userPath, err := NormalizeUserPath(userPath)
	if err != nil {
		return 0, false, err
	}
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "timestamp", Value: bson.D{{Key: "$gte", Value: start.UTC()}, {Key: "$lt", Value: end.UTC()}}},
			{Key: "$and", Value: bson.A{
				mongoUsagePathMatch(userPath),
				mongoUncachedUsageMatch(),
			}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "total", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$total_cost", 0}}}}}},
			{Key: "has_costs", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{bson.D{{Key: "$gt", Value: bson.A{"$total_cost", nil}}}, 1, 0}}}}}},
		}}},
	}
	cursor, err := s.usage.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, false, fmt.Errorf("sum usage cost: %w", err)
	}
	defer cursor.Close(ctx)

	if !cursor.Next(ctx) {
		return 0, false, cursor.Err()
	}
	var row struct {
		Total    float64 `bson:"total"`
		HasCosts int     `bson:"has_costs"`
	}
	if err := cursor.Decode(&row); err != nil {
		return 0, false, fmt.Errorf("decode usage cost sum: %w", err)
	}
	return row.Total, row.HasCosts > 0, nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func usagePathRegex(userPath string) string {
	if userPath == "/" {
		return "^/"
	}
	return "^" + regexp.QuoteMeta(userPath) + "(?:/|$)"
}

func mongoUsagePathMatch(userPath string) bson.D {
	pathPattern := usagePathRegex(userPath)
	if userPath == "/" {
		return bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "user_path", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: `^\s*$`}}}},
			bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: pathPattern}}}},
		}}}
	}
	return bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: pathPattern}}}}
}

func mongoUncachedUsageMatch() bson.D {
	return bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "cache_type", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "cache_type", Value: nil}},
		bson.D{{Key: "cache_type", Value: ""}},
	}}}
}
