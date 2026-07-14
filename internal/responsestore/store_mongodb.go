package responsestore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage"
)

type mongoResponseDocument struct {
	ID        string `bson:"_id"`
	Data      string `bson:"data"`
	StoredAt  int64  `bson:"stored_at"`
	ExpiresAt int64  `bson:"expires_at"`
}

// MongoDBStore persists response snapshots in MongoDB.
type MongoDBStore struct {
	collection  *mongo.Collection
	ttl         time.Duration
	stopCleanup chan struct{}
	closeOnce   sync.Once
}

// NewMongoDBStore creates collection indexes if needed and starts the hourly
// expired-snapshot sweep.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("response_snapshots")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "expires_at", Value: 1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create response_snapshots indexes: %w", err)
	}

	store := &MongoDBStore{
		collection:  coll,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new response snapshot. An existing snapshot with the same id
// is only replaced when it has already expired.
func (s *MongoDBStore) Create(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, true)
	if err != nil {
		return err
	}
	if responseExpired(normalized, now) {
		return nil
	}
	doc := mongoResponseDocument{
		ID:        normalized.Response.ID,
		Data:      string(data),
		StoredAt:  storage.UnixOrZero(normalized.StoredAt),
		ExpiresAt: storage.UnixOrZero(normalized.ExpiresAt),
	}
	// The filter only matches an expired snapshot, so a live one falls through
	// to the upsert insert and surfaces as a duplicate-key conflict.
	filter := bson.M{"_id": doc.ID, "expires_at": bson.M{"$gt": 0, "$lte": now.Unix()}}
	_, err = s.collection.ReplaceOne(ctx, filter, doc, options.Replace().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("response already exists: %s", doc.ID)
	}
	if err != nil {
		return fmt.Errorf("create response snapshot: %w", err)
	}
	return nil
}

// Get retrieves one response snapshot by id.
func (s *MongoDBStore) Get(ctx context.Context, id string) (*StoredResponse, error) {
	var doc mongoResponseDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query response snapshot: %w", err)
	}
	if doc.ExpiresAt > 0 && doc.ExpiresAt <= time.Now().Unix() {
		return nil, ErrNotFound
	}
	return decodeStoredResponse([]byte(doc.Data), doc.StoredAt, doc.ExpiresAt)
}

// Update replaces an existing, unexpired response snapshot. Zero StoredAt or
// ExpiresAt values preserve the stored retention fields.
func (s *MongoDBStore) Update(ctx context.Context, response *StoredResponse) error {
	now := time.Now().UTC()
	normalized, data, err := prepareStoredResponseForStorage(response, now, s.ttl, false)
	if err != nil {
		return err
	}
	set := bson.M{"data": string(data)}
	if !normalized.StoredAt.IsZero() {
		set["stored_at"] = normalized.StoredAt.Unix()
	}
	if !normalized.ExpiresAt.IsZero() {
		set["expires_at"] = normalized.ExpiresAt.Unix()
	}
	result, err := s.collection.UpdateOne(ctx, storage.MongoUnexpiredFilter(normalized.Response.ID, now), bson.M{"$set": set})
	if err != nil {
		return fmt.Errorf("update response snapshot: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one unexpired response snapshot by id.
func (s *MongoDBStore) Delete(ctx context.Context, id string) error {
	result, err := s.collection.DeleteOne(ctx, storage.MongoUnexpiredFilter(id, time.Now()))
	if err != nil {
		return fmt.Errorf("delete response snapshot: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired response snapshots.
func (s *MongoDBStore) DeleteExpired(ctx context.Context) error {
	filter := bson.M{"expires_at": bson.M{"$gt": 0, "$lte": time.Now().Unix()}}
	if _, err := s.collection.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete expired response snapshots: %w", err)
	}
	return nil
}

func (s *MongoDBStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("response snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; client lifecycle is managed by the storage layer.
func (s *MongoDBStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
