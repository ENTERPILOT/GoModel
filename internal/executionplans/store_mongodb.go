package executionplans

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoVersionDocument struct {
	ID            string    `bson:"_id"`
	ScopeProvider string    `bson:"scope_provider,omitempty"`
	ScopeModel    string    `bson:"scope_model,omitempty"`
	ScopeKey      string    `bson:"scope_key"`
	Version       int       `bson:"version"`
	Active        bool      `bson:"active"`
	Name          string    `bson:"name"`
	Description   string    `bson:"description,omitempty"`
	PlanPayload   Payload   `bson:"plan_payload"`
	PlanHash      string    `bson:"plan_hash"`
	CreatedAt     time.Time `bson:"created_at"`
}

// MongoDBStore stores immutable execution-plan versions in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}

	collection := database.Collection("execution_plan_versions")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "scope_key", Value: 1}, {Key: "version", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "scope_key", Value: 1}},
			Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "active", Value: true}}),
		},
		{
			Keys: bson.D{{Key: "active", Value: 1}, {Key: "created_at", Value: -1}},
		},
	}
	if _, err := collection.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create execution plan indexes: %w", err)
	}

	return &MongoDBStore{collection: collection}, nil
}

func (s *MongoDBStore) ListActive(ctx context.Context) ([]Version, error) {
	cursor, err := s.collection.Find(ctx,
		bson.D{{Key: "active", Value: true}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list active execution plans: %w", err)
	}
	defer cursor.Close(ctx)

	versions := make([]Version, 0)
	for cursor.Next(ctx) {
		var doc mongoVersionDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode execution plan: %w", err)
		}
		versions = append(versions, versionFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate execution plans: %w", err)
	}
	return versions, nil
}

func (s *MongoDBStore) Get(ctx context.Context, id string) (*Version, error) {
	var doc mongoVersionDocument
	if err := s.collection.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get execution plan: %w", err)
	}
	version := versionFromMongo(doc)
	return &version, nil
}

func (s *MongoDBStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, planHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	var latest struct {
		Version int `bson:"version"`
	}
	findOpts := options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})
	err = s.collection.FindOne(ctx, bson.D{{Key: "scope_key", Value: scopeKey}}, findOpts).Decode(&latest)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("load latest execution plan version: %w", err)
	}

	if input.Activate {
		if _, err := s.collection.UpdateMany(ctx,
			bson.D{{Key: "scope_key", Value: scopeKey}, {Key: "active", Value: true}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "active", Value: false}}}},
		); err != nil {
			return nil, fmt.Errorf("deactivate current execution plan version: %w", err)
		}
	}

	now := time.Now().UTC()
	version := &Version{
		ID:          uuid.NewString(),
		Scope:       input.Scope,
		ScopeKey:    scopeKey,
		Version:     latest.Version + 1,
		Active:      input.Activate,
		Name:        input.Name,
		Description: input.Description,
		Payload:     input.Payload,
		PlanHash:    planHash,
		CreatedAt:   now,
	}

	if _, err := s.collection.InsertOne(ctx, mongoVersionDocument{
		ID:            version.ID,
		ScopeProvider: version.Scope.Provider,
		ScopeModel:    version.Scope.Model,
		ScopeKey:      version.ScopeKey,
		Version:       version.Version,
		Active:        version.Active,
		Name:          version.Name,
		Description:   version.Description,
		PlanPayload:   version.Payload,
		PlanHash:      version.PlanHash,
		CreatedAt:     version.CreatedAt,
	}); err != nil {
		return nil, fmt.Errorf("insert execution plan version: %w", err)
	}

	return version, nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func versionFromMongo(doc mongoVersionDocument) Version {
	return Version{
		ID: doc.ID,
		Scope: Scope{
			Provider: doc.ScopeProvider,
			Model:    doc.ScopeModel,
		},
		ScopeKey:    doc.ScopeKey,
		Version:     doc.Version,
		Active:      doc.Active,
		Name:        doc.Name,
		Description: doc.Description,
		Payload:     doc.PlanPayload,
		PlanHash:    doc.PlanHash,
		CreatedAt:   doc.CreatedAt.UTC(),
	}
}
