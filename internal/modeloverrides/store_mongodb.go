package modeloverrides

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoOverrideDocument struct {
	ID                      string    `bson:"_id"`
	ProviderName            string    `bson:"provider_name,omitempty"`
	Model                   string    `bson:"model,omitempty"`
	Enabled                 *bool     `bson:"enabled,omitempty"`
	ForceDisabled           bool      `bson:"force_disabled,omitempty"`
	AllowedOnlyForUserPaths []string  `bson:"allowed_only_for_user_paths,omitempty"`
	CreatedAt               time.Time `bson:"created_at"`
	UpdatedAt               time.Time `bson:"updated_at"`
}

type mongoOverrideIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores model overrides in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("model_overrides")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "provider_name", Value: 1}}},
		{Keys: bson.D{{Key: "model", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create model_overrides indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]Override, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list model overrides: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]Override, 0)
	for cursor.Next(ctx) {
		var doc mongoOverrideDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode model override: %w", err)
		}
		result = append(result, overrideFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate model overrides: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, override Override) error {
	override, err := normalizeStoredOverride(override)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if override.CreatedAt.IsZero() {
		override.CreatedAt = now
	}
	override.UpdatedAt = now

	update := bson.M{
		"$set": bson.M{
			"provider_name":               override.ProviderName,
			"model":                       override.Model,
			"enabled":                     override.Enabled,
			"force_disabled":              override.ForceDisabled,
			"allowed_only_for_user_paths": override.AllowedOnlyForUserPaths,
			"updated_at":                  override.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": override.CreatedAt,
		},
	}
	_, err = s.collection.UpdateOne(ctx, mongoOverrideIDFilter{ID: override.Selector}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert model override: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, selector string) error {
	result, err := s.collection.DeleteOne(ctx, mongoOverrideIDFilter{ID: strings.TrimSpace(selector)})
	if err != nil {
		return fmt.Errorf("delete model override: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func overrideFromMongo(doc mongoOverrideDocument) Override {
	return Override{
		Selector:                doc.ID,
		ProviderName:            doc.ProviderName,
		Model:                   doc.Model,
		Enabled:                 cloneEnabled(doc.Enabled),
		ForceDisabled:           doc.ForceDisabled,
		AllowedOnlyForUserPaths: append([]string(nil), doc.AllowedOnlyForUserPaths...),
		CreatedAt:               doc.CreatedAt.UTC(),
		UpdatedAt:               doc.UpdatedAt.UTC(),
	}
}
