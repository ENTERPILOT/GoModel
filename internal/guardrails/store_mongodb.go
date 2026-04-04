package guardrails

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoDefinitionDocument struct {
	Name        string    `bson:"_id"`
	Type        string    `bson:"type"`
	Description string    `bson:"description,omitempty"`
	UserPath    string    `bson:"user_path,omitempty"`
	Config      bson.M    `bson:"config"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

type mongoDefinitionIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores guardrail definitions in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(ctx context.Context, database *mongo.Database) (*MongoDBStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("guardrail_definitions")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "type", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create guardrail indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]Definition, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list guardrails: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]Definition, 0)
	for cursor.Next(ctx) {
		var doc mongoDefinitionDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode guardrail: %w", err)
		}
		definition, err := definitionFromMongo(doc)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate guardrails: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Get(ctx context.Context, name string) (*Definition, error) {
	var doc mongoDefinitionDocument
	err := s.collection.FindOne(ctx, mongoDefinitionIDFilter{ID: normalizeDefinitionName(name)}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get guardrail: %w", err)
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
	definition.UpdatedAt = now

	configDoc, err := mongoConfigFromRaw(definition.Config)
	if err != nil {
		return fmt.Errorf("upsert guardrail: %w", err)
	}

	update := bson.M{
		"$set": bson.M{
			"type":        definition.Type,
			"description": definition.Description,
			"user_path":   definition.UserPath,
			"config":      configDoc,
			"updated_at":  definition.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": definition.CreatedAt,
		},
	}
	_, err = s.collection.UpdateOne(ctx, mongoDefinitionIDFilter{ID: definition.Name}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert guardrail: %w", err)
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
		normalized, err := normalizeDefinition(definition)
		if err != nil {
			return err
		}
		if normalized.CreatedAt.IsZero() {
			normalized.CreatedAt = now
		}
		normalized.UpdatedAt = now

		configDoc, err := mongoConfigFromRaw(normalized.Config)
		if err != nil {
			return fmt.Errorf("upsert guardrail %q: %w", normalized.Name, err)
		}

		update := bson.M{
			"$set": bson.M{
				"type":        normalized.Type,
				"description": normalized.Description,
				"user_path":   normalized.UserPath,
				"config":      configDoc,
				"updated_at":  normalized.UpdatedAt,
			},
			"$setOnInsert": bson.M{
				"created_at": normalized.CreatedAt,
			},
		}
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(mongoDefinitionIDFilter{ID: normalized.Name}).
			SetUpdate(update).
			SetUpsert(true))
	}

	session, err := s.collection.Database().Client().StartSession()
	if err != nil {
		return fmt.Errorf("start guardrail upsert session: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sessionCtx context.Context) (any, error) {
		if _, err := s.collection.BulkWrite(sessionCtx, models, options.BulkWrite().SetOrdered(true)); err != nil {
			return nil, fmt.Errorf("bulk upsert guardrails: %w", err)
		}
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("upsert guardrails: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, name string) error {
	result, err := s.collection.DeleteOne(ctx, mongoDefinitionIDFilter{ID: normalizeDefinitionName(name)})
	if err != nil {
		return fmt.Errorf("delete guardrail: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func mongoConfigFromRaw(raw json.RawMessage) (bson.M, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return bson.M{}, nil
	}
	var doc bson.M
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return nil, fmt.Errorf("decode guardrail config: %w", err)
	}
	if doc == nil {
		return bson.M{}, nil
	}
	return doc, nil
}

func definitionFromMongo(doc mongoDefinitionDocument) (Definition, error) {
	config := doc.Config
	if config == nil {
		config = bson.M{}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return Definition{}, fmt.Errorf("encode guardrail config %q: %w", doc.Name, err)
	}
	return Definition{
		Name:        doc.Name,
		Type:        doc.Type,
		Description: doc.Description,
		UserPath:    doc.UserPath,
		Config:      raw,
		CreatedAt:   doc.CreatedAt.UTC(),
		UpdatedAt:   doc.UpdatedAt.UTC(),
	}, nil
}
