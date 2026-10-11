package metadataoverrides

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores model metadata overrides in MongoDB, one document per
// selector.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore returns a store over the model_metadata_overrides collection.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBStore{collection: database.Collection("model_metadata_overrides")}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]Override, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list model metadata overrides: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]Override, 0)
	for cursor.Next(ctx) {
		var override Override
		if err := cursor.Decode(&override); err != nil {
			return nil, fmt.Errorf("decode model metadata override: %w", err)
		}
		override.CreatedAt = override.CreatedAt.UTC()
		override.UpdatedAt = override.UpdatedAt.UTC()
		result = append(result, override)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate model metadata overrides: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, override Override) error {
	now := time.Now().UTC()
	update := bson.M{
		"$set": bson.M{
			"provider_name": override.ProviderName,
			"model":         override.Model,
			"metadata":      override.Metadata,
			"updated_at":    now,
		},
		"$setOnInsert": bson.M{"created_at": now},
	}
	_, err := s.collection.UpdateOne(ctx, mongoIDFilter{ID: override.Selector}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert model metadata override: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, selector string) error {
	result, err := s.collection.DeleteOne(ctx, mongoIDFilter{ID: strings.TrimSpace(selector)})
	if err != nil {
		return fmt.Errorf("delete model metadata override: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}
