package headerpolicy

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoDefinitionDocument struct {
	Name        string              `bson:"_id"`
	Description string              `bson:"description,omitempty"`
	Config      persistedDefinition `bson:"config"`
	CreatedAt   time.Time           `bson:"created_at"`
	UpdatedAt   time.Time           `bson:"updated_at"`
}

// MongoDBStore persists header policies independently from guardrails.
type MongoDBStore struct{ collection *mongo.Collection }

func NewMongoDBStore(ctx context.Context, database *mongo.Database) (*MongoDBStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	collection := database.Collection("header_policy_definitions")
	indexCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := collection.Indexes().CreateOne(indexCtx, mongo.IndexModel{Keys: bson.D{{Key: "updated_at", Value: -1}}}); err != nil {
		return nil, fmt.Errorf("create header policy indexes: %w", err)
	}
	return &MongoDBStore{collection: collection}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]Definition, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list header policies: %w", err)
	}
	defer cursor.Close(ctx)
	result := make([]Definition, 0)
	for cursor.Next(ctx) {
		var doc mongoDefinitionDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode header policy: %w", err)
		}
		definition, err := definitionFromMongo(doc)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate header policies: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Get(ctx context.Context, name string) (*Definition, error) {
	var doc mongoDefinitionDocument
	if err := s.collection.FindOne(ctx, bson.M{"_id": normalizeDefinitionName(name)}).Decode(&doc); err != nil {
		return nil, storeNotFound(err, mongo.ErrNoDocuments)
	}
	definition, err := definitionFromMongo(doc)
	if err != nil {
		return nil, err
	}
	return &definition, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, definition Definition) error {
	definition, err := normalizeDefinition(definition)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if definition.CreatedAt.IsZero() {
		definition.CreatedAt = now
	}
	update := bson.M{
		"$set": bson.M{
			"description": definition.Description,
			"config":      persistedFromDefinition(definition),
			"updated_at":  now,
		},
		"$setOnInsert": bson.M{"created_at": definition.CreatedAt},
	}
	if _, err := s.collection.UpdateOne(ctx, bson.M{"_id": definition.Name}, update, options.UpdateOne().SetUpsert(true)); err != nil {
		return fmt.Errorf("upsert header policy: %w", err)
	}
	return nil
}

func (s *MongoDBStore) UpsertMany(ctx context.Context, definitions []Definition) error {
	if len(definitions) == 0 {
		return nil
	}
	now := time.Now().UTC()
	models := make([]mongo.WriteModel, 0, len(definitions))
	for _, definition := range definitions {
		definition, err := normalizeDefinition(definition)
		if err != nil {
			return err
		}
		if definition.CreatedAt.IsZero() {
			definition.CreatedAt = now
		}
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": definition.Name}).
			SetUpdate(bson.M{
				"$set":         bson.M{"description": definition.Description, "config": persistedFromDefinition(definition), "updated_at": now},
				"$setOnInsert": bson.M{"created_at": definition.CreatedAt},
			}).
			SetUpsert(true))
	}
	if _, err := s.collection.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(true)); err != nil {
		return fmt.Errorf("upsert header policies: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, name string) error {
	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": normalizeDefinitionName(name)})
	if err != nil {
		return fmt.Errorf("delete header policy: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error { return nil }

func definitionFromMongo(doc mongoDefinitionDocument) (Definition, error) {
	definition, err := definitionFromPersisted(doc.Name, doc.Description, doc.Config)
	if err != nil {
		return Definition{}, err
	}
	definition.CreatedAt = doc.CreatedAt.UTC()
	definition.UpdatedAt = doc.UpdatedAt.UTC()
	return definition, nil
}
