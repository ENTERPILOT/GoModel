package aliases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoAliasDocument struct {
	Name           string    `bson:"_id"`
	TargetModel    string    `bson:"target_model"`
	TargetProvider string    `bson:"target_provider,omitempty"`
	Description    string    `bson:"description,omitempty"`
	Enabled        bool      `bson:"enabled"`
	CreatedAt      time.Time `bson:"created_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

type mongoAliasIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores aliases in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("aliases")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create aliases indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]Alias, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]Alias, 0)
	for cursor.Next(ctx) {
		var doc mongoAliasDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode alias: %w", err)
		}
		result = append(result, aliasFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate aliases: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Get(ctx context.Context, name string) (*Alias, error) {
	var doc mongoAliasDocument
	err := s.collection.FindOne(ctx, mongoAliasIDFilter{ID: normalizeName(name)}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get alias: %w", err)
	}
	alias := aliasFromMongo(doc)
	return &alias, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, alias Alias) error {
	alias, err := normalizeAlias(alias)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if alias.CreatedAt.IsZero() {
		alias.CreatedAt = now
	}
	alias.UpdatedAt = now

	update := bson.M{
		"$set": bson.M{
			"target_model":    alias.TargetModel,
			"target_provider": alias.TargetProvider,
			"description":     alias.Description,
			"enabled":         alias.Enabled,
			"updated_at":      alias.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": alias.CreatedAt,
		},
	}
	_, err = s.collection.UpdateOne(ctx, mongoAliasIDFilter{ID: alias.Name}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert alias: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, name string) error {
	result, err := s.collection.DeleteOne(ctx, mongoAliasIDFilter{ID: normalizeName(name)})
	if err != nil {
		return fmt.Errorf("delete alias: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func aliasFromMongo(doc mongoAliasDocument) Alias {
	return Alias{
		Name:           doc.Name,
		TargetModel:    doc.TargetModel,
		TargetProvider: doc.TargetProvider,
		Description:    doc.Description,
		Enabled:        doc.Enabled,
		CreatedAt:      doc.CreatedAt.UTC(),
		UpdatedAt:      doc.UpdatedAt.UTC(),
	}
}
