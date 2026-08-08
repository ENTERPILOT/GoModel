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
	if err := store.migratePreScopeDocuments(ctx); err != nil {
		return nil, err
	}
	_, err := store.budgets.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "scope", Value: 1}, {Key: "subject", Value: 1}, {Key: "period_seconds", Value: 1}},
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

// migratePreScopeDocuments rewrites budgets stored before budget scopes
// existed (keyed by user_path only) into the scoped shape.
//
// The legacy unique index has to go first. The rewrite unsets user_path, and a
// missing field indexes as null, so every migrated document of the same period
// would collapse onto the key {user_path: null, period_seconds: N} and trip the
// uniqueness constraint — two budgets sharing a period is the ordinary case,
// not an edge one.
func (s *MongoDBStore) migratePreScopeDocuments(ctx context.Context) error {
	// Best-effort: the index does not exist on fresh databases.
	_ = s.budgets.Indexes().DropOne(ctx, "user_path_1_period_seconds_1")

	_, err := s.budgets.UpdateMany(ctx,
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
		return fmt.Errorf("migrate budgets to scoped schema: %w", err)
	}
	return nil
}

func (s *MongoDBStore) ListBudgets(ctx context.Context) ([]Budget, error) {
	cursor, err := s.budgets.Find(ctx, bson.D{}, options.Find().SetSort(
		bson.D{{Key: "scope", Value: 1}, {Key: "subject", Value: 1}, {Key: "period_seconds", Value: 1}}))
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
		filter := mongoBudgetKey(budget.Scope, budget.Subject, budget.PeriodSeconds)
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "scope", Value: budget.Scope},
			{Key: "subject", Value: budget.Subject},
			{Key: "per_child", Value: budget.PerChild},
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

func (s *MongoDBStore) DeleteBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64) error {
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	result, err := s.budgets.DeleteOne(ctx, mongoBudgetKey(scope, subject, periodSeconds))
	if err != nil {
		return fmt.Errorf("delete budget %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
	}
	return nil
}

// mongoBudgetKey is the document filter identifying one budget.
func mongoBudgetKey(scope Scope, subject string, periodSeconds int64) bson.D {
	return bson.D{
		{Key: "scope", Value: scope},
		{Key: "subject", Value: subject},
		{Key: "period_seconds", Value: periodSeconds},
	}
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
			keep = append(keep, mongoBudgetKey(budget.Scope, budget.Subject, budget.PeriodSeconds))
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
		keys = append(keys, mongoBudgetKey(budget.Scope, budget.Subject, budget.PeriodSeconds))
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
		existingSources[budgetKey(existing.Scope, existing.Subject, existing.PeriodSeconds)] = existing.Source
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing budget collisions: %w", err)
	}

	filtered := make([]Budget, 0, len(budgets))
	for _, budget := range budgets {
		if source, ok := existingSources[budgetKey(budget.Scope, budget.Subject, budget.PeriodSeconds)]; ok && source != "" && source != SourceConfig {
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

func (s *MongoDBStore) ResetBudget(ctx context.Context, scope Scope, subject string, periodSeconds int64, at time.Time) error {
	scope, subject, err := normalizeBudgetKey(scope, subject, periodSeconds)
	if err != nil {
		return err
	}
	result, err := s.budgets.UpdateOne(ctx,
		mongoBudgetKey(scope, subject, periodSeconds),
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "last_reset_at", Value: at.UTC()},
			{Key: "updated_at", Value: at.UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("reset budget %s %s/%d: %w", scope, subject, periodSeconds, err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("%w: %s %s/%d", ErrNotFound, scope, subject, periodSeconds)
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

// SumSpend totals uncached spend for every window in one aggregation.
//
// A budget check matches several budgets at once — a user-path subtree plus a
// budget per request label — so the pipeline matches the union of their windows
// once and accumulates a conditional sum per window instead of running an
// aggregation each.
func (s *MongoDBStore) SumSpend(ctx context.Context, windows []SpendWindow) ([]Spend, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	group := bson.D{{Key: "_id", Value: nil}}
	for i, window := range windows {
		condition, err := mongoSpendCondition(window)
		if err != nil {
			return nil, err
		}
		group = append(group,
			bson.E{Key: spendTotalField(i), Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				condition,
				bson.D{{Key: "$ifNull", Value: bson.A{"$total_cost", 0}}},
				0,
			}}}}}},
			bson.E{Key: spendHasUsageField(i), Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$and", Value: bson.A{condition, bson.D{{Key: "$gt", Value: bson.A{"$total_cost", nil}}}}}},
				1,
				0,
			}}}}}},
		)
	}

	minStart, maxEnd := spendBounds(windows)
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "timestamp", Value: bson.D{{Key: "$gte", Value: minStart.UTC()}, {Key: "$lt", Value: maxEnd.UTC()}}},
			{Key: "$or", Value: bson.A{
				bson.D{{Key: "cache_type", Value: bson.D{{Key: "$exists", Value: false}}}},
				bson.D{{Key: "cache_type", Value: nil}},
				bson.D{{Key: "cache_type", Value: ""}},
			}},
		}}},
		bson.D{{Key: "$group", Value: group}},
	}
	cursor, err := s.usage.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("sum usage cost: %w", err)
	}
	defer cursor.Close(ctx)

	spends := make([]Spend, len(windows))
	if !cursor.Next(ctx) {
		// No usage in the union window at all: every budget is untouched.
		return spends, cursor.Err()
	}
	var row bson.M
	if err := cursor.Decode(&row); err != nil {
		return nil, fmt.Errorf("decode usage cost sum: %w", err)
	}
	for i := range windows {
		total, _ := bsonNumber(row[spendTotalField(i)])
		hasUsage, _ := bsonNumber(row[spendHasUsageField(i)])
		spends[i] = Spend{Total: total, HasUsage: hasUsage > 0}
	}
	return spends, nil
}

func spendTotalField(i int) string    { return "total_" + strconv.Itoa(i) }
func spendHasUsageField(i int) string { return "priced_" + strconv.Itoa(i) }

// bsonNumber reads a numeric aggregation result, which the server may return
// as an int32, int64, or double depending on the values it summed.
func bsonNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	default:
		return 0, false
	}
}

// mongoSpendCondition renders the per-document predicate for one window as an
// aggregation expression: inside the time window and matching the subject.
func mongoSpendCondition(window SpendWindow) (bson.D, error) {
	subjectMatch, err := mongoSubjectMatch(window)
	if err != nil {
		return nil, err
	}
	return bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "$gte", Value: bson.A{"$timestamp", window.Start.UTC()}}},
		bson.D{{Key: "$lt", Value: bson.A{"$timestamp", window.End.UTC()}}},
		subjectMatch,
	}}}, nil
}

func mongoSubjectMatch(window SpendWindow) (bson.D, error) {
	if window.Scope == ScopeLabel {
		subject, err := NormalizeSubject(ScopeLabel, window.Subject)
		if err != nil {
			return nil, err
		}
		return bson.D{{Key: "$in", Value: bson.A{subject, bson.D{{Key: "$ifNull", Value: bson.A{"$labels", bson.A{}}}}}}}, nil
	}
	userPath, err := NormalizeUserPath(window.Subject)
	if err != nil {
		return nil, err
	}
	// Missing and blank user paths count as the root, matching the SQL stores.
	normalized := bson.D{{Key: "$let", Value: bson.D{
		{Key: "vars", Value: bson.D{{Key: "path", Value: bson.D{{Key: "$trim", Value: bson.D{
			{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$user_path", ""}}}},
		}}}}}},
		{Key: "in", Value: bson.D{{Key: "$cond", Value: bson.A{
			bson.D{{Key: "$eq", Value: bson.A{"$$path", ""}}}, "/", "$$path",
		}}}},
	}}}
	return bson.D{{Key: "$regexMatch", Value: bson.D{
		{Key: "input", Value: normalized},
		{Key: "regex", Value: usagePathRegex(userPath)},
	}}}, nil
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
