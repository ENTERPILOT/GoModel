package workflows

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoVersionDocument struct {
	ID              string    `bson:"_id"`
	ScopeProvider   string    `bson:"scope_provider,omitempty"`
	ScopeModel      string    `bson:"scope_model,omitempty"`
	ScopeUserPath   string    `bson:"scope_user_path,omitempty"`
	ScopeKey        string    `bson:"scope_key"`
	Version         int       `bson:"version"`
	Active          bool      `bson:"active"`
	Managed         bool      `bson:"managed_default,omitempty"`
	Name            string    `bson:"name"`
	Description     string    `bson:"description,omitempty"`
	WorkflowPayload Payload   `bson:"workflow_payload"`
	WorkflowHash    string    `bson:"workflow_hash"`
	CreatedAt       time.Time `bson:"created_at"`
}

// MongoDBStore stores immutable workflow versions in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}

	collection := database.Collection("workflow_versions")
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
		return nil, fmt.Errorf("create workflow indexes: %w", err)
	}
	if _, err := collection.UpdateMany(ctx,
		bson.D{
			{Key: "managed_default", Value: bson.D{{Key: "$ne", Value: true}}},
			{Key: "scope_key", Value: "global"},
			{Key: "name", Value: ManagedDefaultGlobalName},
			{Key: "description", Value: ManagedDefaultGlobalDescription},
		},
		bson.D{{Key: "$set", Value: bson.D{{Key: "managed_default", Value: true}}}},
	); err != nil {
		return nil, fmt.Errorf("backfill managed workflow defaults: %w", err)
	}

	return &MongoDBStore{collection: collection}, nil
}

func (s *MongoDBStore) ListActive(ctx context.Context) ([]Version, error) {
	cursor, err := s.collection.Find(ctx,
		bson.D{{Key: "active", Value: true}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list active workflows: %w", err)
	}
	defer cursor.Close(ctx)

	versions := make([]Version, 0)
	for cursor.Next(ctx) {
		var doc mongoVersionDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode workflow: %w", err)
		}
		versions = append(versions, versionFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflows: %w", err)
	}
	return versions, nil
}

func (s *MongoDBStore) Get(ctx context.Context, id string) (*Version, error) {
	var doc mongoVersionDocument
	if err := s.collection.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get workflow: %w", err)
	}
	version := versionFromMongo(doc)
	return &version, nil
}

func (s *MongoDBStore) Create(ctx context.Context, input CreateInput) (*Version, error) {
	input, scopeKey, workflowHash, err := normalizeCreateInput(input)
	if err != nil {
		return nil, err
	}

	session, err := s.collection.Database().Client().StartSession()
	if err != nil {
		return nil, fmt.Errorf("start workflow session: %w", err)
	}
	defer session.EndSession(ctx)

	result, err := session.WithTransaction(ctx, func(sessionCtx context.Context) (any, error) {
		var latest struct {
			Version int `bson:"version"`
		}
		findOpts := options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})
		err := s.collection.FindOne(sessionCtx, bson.D{{Key: "scope_key", Value: scopeKey}}, findOpts).Decode(&latest)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("load latest workflow version: %w", err)
		}

		if input.Activate {
			if _, err := s.collection.UpdateMany(sessionCtx,
				bson.D{{Key: "scope_key", Value: scopeKey}, {Key: "active", Value: true}},
				bson.D{{Key: "$set", Value: bson.D{{Key: "active", Value: false}}}},
			); err != nil {
				return nil, fmt.Errorf("deactivate current workflow version: %w", err)
			}
		}

		now := time.Now().UTC()
		version := &Version{
			ID:           uuid.NewString(),
			Scope:        input.Scope,
			ScopeKey:     scopeKey,
			Version:      latest.Version + 1,
			Active:       input.Activate,
			Managed:      input.Managed,
			Name:         input.Name,
			Description:  input.Description,
			Payload:      input.Payload,
			WorkflowHash: workflowHash,
			CreatedAt:    now,
		}

		if err := s.insertVersion(sessionCtx, version); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return nil, fmt.Errorf("insert workflow version: duplicate key: %w", err)
			}
			return nil, fmt.Errorf("insert workflow version: %w", err)
		}

		return version, nil
	})
	if err != nil {
		return nil, err
	}

	version, ok := result.(*Version)
	if !ok {
		return nil, fmt.Errorf("unexpected workflow transaction result: %T", result)
	}
	return version, nil
}

func (s *MongoDBStore) EnsureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error) {
	var lastErr error
	for range 5 {
		version, err := s.ensureManagedDefaultGlobal(ctx, input, workflowHash)
		if err == nil {
			return version, nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ensure managed default workflow after concurrent retries: %w", lastErr)
}

func (s *MongoDBStore) Deactivate(ctx context.Context, id string) error {
	result, err := s.collection.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "active", Value: true}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "active", Value: false}}}},
	)
	if err != nil {
		return fmt.Errorf("deactivate workflow version: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func (s *MongoDBStore) ensureManagedDefaultGlobal(ctx context.Context, input CreateInput, workflowHash string) (*Version, error) {
	session, err := s.collection.Database().Client().StartSession()
	if err != nil {
		return nil, fmt.Errorf("start workflow session: %w", err)
	}
	defer session.EndSession(ctx)

	result, err := session.WithTransaction(ctx, func(sessionCtx context.Context) (any, error) {
		var activeDoc mongoVersionDocument
		err := s.collection.FindOne(sessionCtx,
			bson.D{{Key: "scope_key", Value: "global"}, {Key: "active", Value: true}},
			options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}),
		).Decode(&activeDoc)
		hasActive := true
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				hasActive = false
			} else {
				return nil, fmt.Errorf("load active global workflow: %w", err)
			}
		}

		if hasActive {
			if !activeDoc.Managed {
				return nil, nil
			}
			if strings.TrimSpace(activeDoc.Name) == input.Name &&
				strings.TrimSpace(activeDoc.Description) == input.Description &&
				strings.TrimSpace(activeDoc.WorkflowHash) == workflowHash {
				return nil, nil
			}
		}

		var latest struct {
			Version int `bson:"version"`
		}
		findOpts := options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})
		err = s.collection.FindOne(sessionCtx, bson.D{{Key: "scope_key", Value: "global"}}, findOpts).Decode(&latest)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("load latest workflow version: %w", err)
		}

		if hasActive {
			if _, err := s.collection.UpdateOne(sessionCtx,
				bson.D{{Key: "_id", Value: activeDoc.ID}, {Key: "active", Value: true}},
				bson.D{{Key: "$set", Value: bson.D{{Key: "active", Value: false}}}},
			); err != nil {
				return nil, fmt.Errorf("deactivate current workflow version: %w", err)
			}
		}

		now := time.Now().UTC()
		version := &Version{
			ID:           uuid.NewString(),
			Scope:        input.Scope,
			ScopeKey:     "global",
			Version:      latest.Version + 1,
			Active:       true,
			Managed:      true,
			Name:         input.Name,
			Description:  input.Description,
			Payload:      input.Payload,
			WorkflowHash: workflowHash,
			CreatedAt:    now,
		}

		if err := s.insertVersion(sessionCtx, version); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return nil, fmt.Errorf("insert workflow version: duplicate key: %w", err)
			}
			return nil, fmt.Errorf("insert workflow version: %w", err)
		}

		return version, nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	version, ok := result.(*Version)
	if !ok {
		return nil, fmt.Errorf("unexpected workflow transaction result: %T", result)
	}
	return version, nil
}

func (s *MongoDBStore) insertVersion(ctx context.Context, version *Version) error {
	if version == nil {
		return fmt.Errorf("version is required")
	}
	_, err := s.collection.InsertOne(ctx, mongoVersionDocument{
		ID:              version.ID,
		ScopeProvider:   version.Scope.Provider,
		ScopeModel:      version.Scope.Model,
		ScopeUserPath:   version.Scope.UserPath,
		ScopeKey:        version.ScopeKey,
		Version:         version.Version,
		Active:          version.Active,
		Managed:         version.Managed,
		Name:            version.Name,
		Description:     version.Description,
		WorkflowPayload: version.Payload,
		WorkflowHash:    version.WorkflowHash,
		CreatedAt:       version.CreatedAt,
	})
	return err
}

func versionFromMongo(doc mongoVersionDocument) Version {
	return Version{
		ID: doc.ID,
		Scope: Scope{
			Provider: doc.ScopeProvider,
			Model:    doc.ScopeModel,
			UserPath: doc.ScopeUserPath,
		},
		ScopeKey:     doc.ScopeKey,
		Version:      doc.Version,
		Active:       doc.Active,
		Managed:      doc.Managed,
		Name:         doc.Name,
		Description:  doc.Description,
		Payload:      doc.WorkflowPayload,
		WorkflowHash: doc.WorkflowHash,
		CreatedAt:    doc.CreatedAt.UTC(),
	}
}
