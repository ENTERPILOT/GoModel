package tagging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoDBStore persists tagging rules in a settings collection.
type MongoDBStore struct {
	settings *mongo.Collection
}

// NewMongoDBStore creates a tagging store over the tagging_settings collection.
func NewMongoDBStore(_ context.Context, database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBStore{settings: database.Collection("tagging_settings")}, nil
}

type mongoRulesDocument struct {
	Key       string `bson:"_id"`
	Rules     []Rule `bson:"rules"`
	UpdatedAt int64  `bson:"updated_at"`
}

func (s *MongoDBStore) GetRules(ctx context.Context) ([]Rule, error) {
	var doc mongoRulesDocument
	err := s.settings.FindOne(ctx, bson.D{{Key: "_id", Value: rulesSettingKey}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tagging rules: %w", err)
	}
	return doc.Rules, nil
}

func (s *MongoDBStore) SaveRules(ctx context.Context, rules []Rule) error {
	if rules == nil {
		rules = []Rule{}
	}
	doc := mongoRulesDocument{Key: rulesSettingKey, Rules: rules, UpdatedAt: time.Now().Unix()}
	_, err := s.settings.ReplaceOne(ctx, bson.D{{Key: "_id", Value: rulesSettingKey}}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("save tagging rules: %w", err)
	}
	return nil
}

// Close is a no-op: the client is managed by the storage layer.
func (s *MongoDBStore) Close() error {
	return nil
}
