package providers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoCredentialDocument struct {
	ID                       string    `bson:"_id"`
	Type                     string    `bson:"type"`
	APIKeys                  []string  `bson:"api_keys,omitempty"`
	BaseURL                  string    `bson:"base_url,omitempty"`
	APIVersion               string    `bson:"api_version,omitempty"`
	Backend                  string    `bson:"backend,omitempty"`
	AuthType                 string    `bson:"auth_type,omitempty"`
	APIMode                  string    `bson:"api_mode,omitempty"`
	VertexProject            string    `bson:"vertex_project,omitempty"`
	VertexLocation           string    `bson:"vertex_location,omitempty"`
	ServiceAccountFile       string    `bson:"service_account_file,omitempty"`
	ServiceAccountJSON       string    `bson:"service_account_json,omitempty"`
	ServiceAccountJSONBase64 string    `bson:"service_account_json_base64,omitempty"`
	GCPScope                 string    `bson:"gcp_scope,omitempty"`
	Models                   []string  `bson:"models,omitempty"`
	Enabled                  bool      `bson:"enabled"`
	CreatedAt                time.Time `bson:"created_at"`
	UpdatedAt                time.Time `bson:"updated_at"`
}

type mongoCredentialIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBCredentialStore stores admin-managed provider credentials in
// MongoDB.
type MongoDBCredentialStore struct {
	collection *mongo.Collection
}

// NewMongoDBCredentialStore creates collection indexes if needed.
func NewMongoDBCredentialStore(database *mongo.Database) (*MongoDBCredentialStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("provider_credentials")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
		{Keys: bson.D{{Key: "updated_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create provider_credentials indexes: %w", err)
	}
	return &MongoDBCredentialStore{collection: coll}, nil
}

func (s *MongoDBCredentialStore) List(ctx context.Context) ([]ManagedProviderCredential, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list provider credentials: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]ManagedProviderCredential, 0)
	for cursor.Next(ctx) {
		var doc mongoCredentialDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode provider credential: %w", err)
		}
		result = append(result, credentialFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider credentials: %w", err)
	}
	return result, nil
}

func (s *MongoDBCredentialStore) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	var doc mongoCredentialDocument
	err := s.collection.FindOne(ctx, mongoCredentialIDFilter{ID: normalizeCredentialName(name)}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrCredentialNotFound
		}
		return nil, fmt.Errorf("get provider credential: %w", err)
	}
	cred := credentialFromMongo(doc)
	return &cred, nil
}

func (s *MongoDBCredentialStore) Upsert(ctx context.Context, cred ManagedProviderCredential) error {
	stampCredentialUpsert(&cred)
	update := bson.M{
		"$set": bson.M{
			"type":                        cred.Type,
			"api_keys":                    cred.APIKeys,
			"base_url":                    cred.BaseURL,
			"api_version":                 cred.APIVersion,
			"backend":                     cred.Backend,
			"auth_type":                   cred.AuthType,
			"api_mode":                    cred.APIMode,
			"vertex_project":              cred.VertexProject,
			"vertex_location":             cred.VertexLocation,
			"service_account_file":        cred.ServiceAccountFile,
			"service_account_json":        cred.ServiceAccountJSON,
			"service_account_json_base64": cred.ServiceAccountJSONBase64,
			"gcp_scope":                   cred.GCPScope,
			"models":                      cred.Models,
			"enabled":                     cred.Enabled,
			"updated_at":                  cred.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at": cred.CreatedAt,
		},
	}
	_, err := s.collection.UpdateOne(ctx, mongoCredentialIDFilter{ID: normalizeCredentialName(cred.Name)}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert provider credential: %w", err)
	}
	return nil
}

func (s *MongoDBCredentialStore) Delete(ctx context.Context, name string) error {
	result, err := s.collection.DeleteOne(ctx, mongoCredentialIDFilter{ID: normalizeCredentialName(name)})
	if err != nil {
		return fmt.Errorf("delete provider credential: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (s *MongoDBCredentialStore) Close() error {
	return nil
}

func credentialFromMongo(doc mongoCredentialDocument) ManagedProviderCredential {
	cred := ManagedProviderCredential{
		Name:                     doc.ID,
		Type:                     doc.Type,
		BaseURL:                  doc.BaseURL,
		APIVersion:               doc.APIVersion,
		Backend:                  doc.Backend,
		AuthType:                 doc.AuthType,
		APIMode:                  doc.APIMode,
		VertexProject:            doc.VertexProject,
		VertexLocation:           doc.VertexLocation,
		ServiceAccountFile:       doc.ServiceAccountFile,
		ServiceAccountJSON:       doc.ServiceAccountJSON,
		ServiceAccountJSONBase64: doc.ServiceAccountJSONBase64,
		GCPScope:                 doc.GCPScope,
		Enabled:                  doc.Enabled,
		CreatedAt:                doc.CreatedAt.UTC(),
		UpdatedAt:                doc.UpdatedAt.UTC(),
	}
	if len(doc.APIKeys) > 0 {
		cred.APIKeys = append([]string(nil), doc.APIKeys...)
	}
	if len(doc.Models) > 0 {
		cred.Models = append([]string(nil), doc.Models...)
	}
	return cred
}
