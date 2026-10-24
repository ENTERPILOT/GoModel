package mediastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage"
)

type mongoObjectDocument struct {
	ID          string `bson:"_id"`
	Kind        string `bson:"kind"`
	Source      string `bson:"source"`
	ContentType string `bson:"content_type"`
	Bytes       int64  `bson:"bytes"`
	StorageKey  string `bson:"storage_key"`
	RequestID   string `bson:"request_id,omitempty"`
	UserPath    string `bson:"user_path,omitempty"`
	CreatedAt   int64  `bson:"created_at"`
	ExpiresAt   int64  `bson:"expires_at"`
}

func (d *mongoObjectDocument) object() *Object {
	return &Object{
		ID:          d.ID,
		Kind:        Kind(d.Kind),
		Source:      Source(d.Source),
		ContentType: d.ContentType,
		Bytes:       d.Bytes,
		StorageKey:  d.StorageKey,
		RequestID:   d.RequestID,
		UserPath:    d.UserPath,
		CreatedAt:   time.Unix(d.CreatedAt, 0).UTC(),
		ExpiresAt:   storage.UnixTime(d.ExpiresAt),
	}
}

// MongoDBStore keeps media records in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	coll := database.Collection("media_objects")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "expires_at", Value: 1}}},
		{Keys: bson.D{{Key: "request_id", Value: 1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create media_objects indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

// Insert stores a record; an existing id is replaced.
func (s *MongoDBStore) Insert(ctx context.Context, object *Object) error {
	normalized, err := normalizeObject(object)
	if err != nil {
		return err
	}
	doc := mongoObjectDocument{
		ID:          normalized.ID,
		Kind:        string(normalized.Kind),
		Source:      string(normalized.Source),
		ContentType: normalized.ContentType,
		Bytes:       normalized.Bytes,
		StorageKey:  normalized.StorageKey,
		RequestID:   normalized.RequestID,
		UserPath:    normalized.UserPath,
		CreatedAt:   normalized.CreatedAt.Unix(),
		ExpiresAt:   storage.UnixOrZero(normalized.ExpiresAt),
	}
	opts := options.Replace().SetUpsert(true)
	if _, err := s.collection.ReplaceOne(ctx, bson.M{"_id": doc.ID}, doc, opts); err != nil {
		return fmt.Errorf("insert media object: %w", err)
	}
	return nil
}

// Get returns one record by id.
func (s *MongoDBStore) Get(ctx context.Context, id string) (*Object, error) {
	var doc mongoObjectDocument
	if err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query media object: %w", err)
	}
	return doc.object(), nil
}

// Delete removes one record by id.
func (s *MongoDBStore) Delete(ctx context.Context, id string) error {
	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("delete media object: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Expired returns expired records, oldest expiry first.
func (s *MongoDBStore) Expired(ctx context.Context, now time.Time, limit int) ([]*Object, error) {
	if limit <= 0 {
		limit = 100
	}
	filter := bson.M{"expires_at": bson.M{"$gt": 0, "$lte": now.Unix()}}
	opts := options.Find().
		SetSort(bson.D{{Key: "expires_at", Value: 1}, {Key: "_id", Value: 1}}).
		SetLimit(int64(limit))
	cursor, err := s.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list expired media objects: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	objects := make([]*Object, 0, limit)
	for cursor.Next(ctx) {
		var doc mongoObjectDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode media object: %w", err)
		}
		objects = append(objects, doc.object())
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate media objects: %w", err)
	}
	return objects, nil
}

// Close is a no-op; client lifecycle is managed by the storage layer.
func (s *MongoDBStore) Close() error {
	return nil
}
