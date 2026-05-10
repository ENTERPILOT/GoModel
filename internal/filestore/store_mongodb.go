package filestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoFileDocument struct {
	ID           string `bson:"_id"`
	ProviderType string `bson:"provider_type"`
	Purpose      string `bson:"purpose,omitempty"`
	Filename     string `bson:"filename,omitempty"`
	Bytes        int64  `bson:"bytes,omitempty"`
	CreatedAt    int64  `bson:"created_at,omitempty"`
	UpdatedAt    int64  `bson:"updated_at"`
	UserPath     string `bson:"user_path,omitempty"`
}

// MongoDBStore stores file provider mappings in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("file_mappings")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "provider_type", Value: 1}}},
		{Keys: bson.D{{Key: "created_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create file_mappings indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

// Upsert creates or replaces a file mapping.
func (s *MongoDBStore) Upsert(ctx context.Context, file *StoredFile) error {
	normalized, err := normalizeStoredFile(file)
	if err != nil {
		return err
	}
	opts := options.UpdateOne().SetUpsert(true)
	update := bson.M{
		"$set": bson.M{
			"provider_type": normalized.ProviderType,
			"purpose":       normalized.Purpose,
			"filename":      normalized.Filename,
			"bytes":         normalized.Bytes,
			"updated_at":    time.Now().Unix(),
			"user_path":     normalized.UserPath,
		},
		"$setOnInsert": bson.M{"_id": normalized.ID, "created_at": normalized.CreatedAt},
	}
	if _, err := s.collection.UpdateOne(ctx, bson.M{"_id": normalized.ID}, update, opts); err != nil {
		return fmt.Errorf("upsert file mapping: %w", err)
	}
	return nil
}

// Get retrieves one file mapping by id.
func (s *MongoDBStore) Get(ctx context.Context, id string) (*StoredFile, error) {
	var doc mongoFileDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query file mapping: %w", err)
	}
	return cloneStoredFile(&StoredFile{
		ID:           doc.ID,
		ProviderType: doc.ProviderType,
		Purpose:      doc.Purpose,
		Filename:     doc.Filename,
		Bytes:        doc.Bytes,
		CreatedAt:    doc.CreatedAt,
		UserPath:     doc.UserPath,
	})
}

// Delete removes one file mapping by id.
func (s *MongoDBStore) Delete(ctx context.Context, id string) error {
	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("delete file mapping: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; Mongo client lifecycle is managed by storage layer.
func (s *MongoDBStore) Close() error {
	return nil
}
