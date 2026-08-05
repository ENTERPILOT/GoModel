package runtimesettings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoDBStore persists runtime settings in MongoDB.
type MongoDBStore struct{ settings *mongo.Collection }

type mongoDocument struct {
	Key       string `bson:"_id"`
	Value     string `bson:"value"`
	UpdatedAt int64  `bson:"updated_at"`
}

// NewMongoDBStore uses the shared runtime_settings collection.
func NewMongoDBStore(_ context.Context, database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBStore{settings: database.Collection("runtime_settings")}, nil
}

// Get returns a persisted value when present.
func (s *MongoDBStore) Get(ctx context.Context, key string) (string, bool, error) {
	var doc mongoDocument
	err := s.settings.FindOne(ctx, bson.D{{Key: "_id", Value: key}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get runtime setting %q: %w", key, err)
	}
	return doc.Value, true, nil
}

// Set upserts a persisted value.
func (s *MongoDBStore) Set(ctx context.Context, key, value string) error {
	doc := mongoDocument{Key: key, Value: value, UpdatedAt: time.Now().Unix()}
	_, err := s.settings.ReplaceOne(ctx, bson.D{{Key: "_id", Value: key}}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("save runtime setting %q: %w", key, err)
	}
	return nil
}
