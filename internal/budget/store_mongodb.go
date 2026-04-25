package budget

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
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
		{Keys: bson.D{{Key: "user_path", Value: 1}}},
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
	if len(budgets) == 0 {
		return nil
	}
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
		if _, err := s.budgets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
			return fmt.Errorf("upsert budget %s/%d: %w", budget.UserPath, budget.PeriodSeconds, err)
		}
	}
	return nil
}

func (s *MongoDBStore) ReplaceConfigBudgets(ctx context.Context, budgets []Budget) error {
	budgets, err := normalizeBudgetsForUpsert(budgets)
	if err != nil {
		return err
	}
	for i := range budgets {
		budgets[i].Source = "config"
	}

	filter := bson.D{{Key: "source", Value: "config"}}
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
	return s.UpsertBudgets(ctx, budgets)
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
	for key, value := range settingsKeyValues(settings) {
		filter := bson.D{{Key: "key", Value: key}}
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "key", Value: key},
			{Key: "value", Value: strconv.Itoa(value)},
			{Key: "updated_at", Value: settings.UpdatedAt},
		}}}
		if _, err := s.settings.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
			return Settings{}, fmt.Errorf("save budget setting %s: %w", key, err)
		}
	}
	return settings, nil
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
	pathPattern := usagePathRegex(userPath)
	pipeline := bson.A{
		bson.D{{Key: "$addFields", Value: bson.D{
			{Key: "_gomodel_budget_user_path", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$ne", Value: bson.A{bson.D{{Key: "$trim", Value: bson.D{{Key: "input", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$user_path", ""}}}}}}}, ""}}},
				bson.D{{Key: "$trim", Value: bson.D{{Key: "input", Value: "$user_path"}}}},
				"/",
			}}}},
		}}},
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "timestamp", Value: bson.D{{Key: "$gte", Value: start.UTC()}, {Key: "$lt", Value: end.UTC()}}},
			{Key: "_gomodel_budget_user_path", Value: bson.D{{Key: "$regex", Value: pathPattern}}},
			{Key: "$or", Value: bson.A{
				bson.D{{Key: "cache_type", Value: bson.D{{Key: "$exists", Value: false}}}},
				bson.D{{Key: "cache_type", Value: ""}},
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
