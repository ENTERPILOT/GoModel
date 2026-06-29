package failover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoDBStore struct {
	collection *mongo.Collection
}

func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("failover_rules")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create failover_rules indexes: %w", err)
	}
	if err := migrateMongoDBFailoverRules(ctx, coll); err != nil {
		return nil, err
	}
	return &MongoDBStore{collection: coll}, nil
}

func migrateMongoDBFailoverRules(ctx context.Context, coll *mongo.Collection) error {
	if _, err := coll.UpdateMany(ctx,
		bson.M{"fallback_models": bson.M{"$exists": false}, "targets": bson.M{"$exists": true}},
		mongo.Pipeline{
			bson.D{{Key: "$set", Value: bson.D{{Key: "fallback_models", Value: "$targets"}}}},
		},
	); err != nil {
		return fmt.Errorf("migrate failover_rules targets field: %w", err)
	}
	if _, err := coll.UpdateMany(ctx,
		bson.M{"$or": bson.A{
			bson.M{"targets": bson.M{"$exists": true}},
			bson.M{"description": bson.M{"$exists": true}},
		}},
		bson.M{"$unset": bson.M{"targets": "", "description": ""}},
	); err != nil {
		return fmt.Errorf("remove legacy failover_rules fields: %w", err)
	}
	return nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]Rule, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list failover mappings: %w", err)
	}
	defer cursor.Close(ctx)
	result := make([]Rule, 0)
	for cursor.Next(ctx) {
		var rule Rule
		if err := cursor.Decode(&rule); err != nil {
			return nil, fmt.Errorf("decode failover mapping: %w", err)
		}
		rule.Source = strings.TrimSpace(rule.Source)
		result = append(result, rule.clone())
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate failover mappings: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Get(ctx context.Context, source string) (*Rule, error) {
	var rule Rule
	err := s.collection.FindOne(ctx, bson.M{"_id": strings.TrimSpace(source)}).Decode(&rule)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get failover mapping: %w", err)
	}
	rule.Source = strings.TrimSpace(rule.Source)
	return &rule, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, rule Rule) error {
	stampUpsert(&rule)
	update := bson.M{
		"$set": bson.M{
			"fallback_models": rule.Targets,
			"enabled":         rule.Enabled,
			"managed_source":  rule.ManagedSource,
			"updated_at":      rule.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": rule.CreatedAt,
		},
	}
	_, err := s.collection.UpdateOne(ctx, bson.M{"_id": strings.TrimSpace(rule.Source)}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert failover mapping: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, source string) error {
	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": strings.TrimSpace(source)})
	if err != nil {
		return fmt.Errorf("delete failover mapping: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) DeleteAll(ctx context.Context) error {
	if _, err := s.collection.DeleteMany(ctx, bson.M{}); err != nil {
		return fmt.Errorf("delete failover mappings: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Close() error { return nil }
