package conversationstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage"
)

// mongoConversationDocument stores the snapshot JSON and one JSON string per
// item, so appends stay atomic ($push) and item JSON round-trips untouched.
type mongoConversationDocument struct {
	ID        string   `bson:"_id"`
	Data      string   `bson:"data"`
	Items     []string `bson:"items"`
	StoredAt  int64    `bson:"stored_at"`
	ExpiresAt int64    `bson:"expires_at"`
}

// MongoDBStore persists conversation snapshots in MongoDB.
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
	coll := database.Collection("conversation_snapshots")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "expires_at", Value: 1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create conversation_snapshots indexes: %w", err)
	}

	store := &MongoDBStore{
		collection:  coll,
		ttl:         DefaultPersistentStoreTTL,
		stopCleanup: make(chan struct{}),
	}
	go storage.RunCleanupLoop(store.stopCleanup, CleanupInterval, store.cleanup)
	return store, nil
}

// Create stores a new conversation snapshot. An existing snapshot with the
// same id is only replaced when it has already expired.
func (s *MongoDBStore) Create(ctx context.Context, conversation *StoredConversation) error {
	now := time.Now().UTC()
	normalized, data, _, err := prepareStoredConversationForStorage(conversation, now, s.ttl, true)
	if err != nil {
		return err
	}
	if conversationExpired(normalized, now) {
		return nil
	}
	doc := mongoConversationDocument{
		ID:        normalized.Conversation.ID,
		Data:      string(data),
		Items:     itemsToStrings(normalized.Items),
		StoredAt:  storage.UnixOrZero(normalized.StoredAt),
		ExpiresAt: storage.UnixOrZero(normalized.ExpiresAt),
	}
	// The filter only matches an expired snapshot, so a live one falls through
	// to the upsert insert and surfaces as a duplicate-key conflict.
	filter := bson.M{"_id": doc.ID, "expires_at": bson.M{"$gt": 0, "$lte": now.Unix()}}
	_, err = s.collection.ReplaceOne(ctx, filter, doc, options.Replace().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("conversation already exists: %s", doc.ID)
	}
	if err != nil {
		return fmt.Errorf("create conversation snapshot: %w", err)
	}
	return nil
}

// Get retrieves one conversation snapshot by id.
func (s *MongoDBStore) Get(ctx context.Context, id string) (*StoredConversation, error) {
	var doc mongoConversationDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query conversation snapshot: %w", err)
	}
	if doc.ExpiresAt > 0 && doc.ExpiresAt <= time.Now().Unix() {
		return nil, ErrNotFound
	}
	stored, err := decodeStoredConversation([]byte(doc.Data), nil, doc.StoredAt, doc.ExpiresAt)
	if err != nil {
		return nil, err
	}
	stored.Items = itemsFromStrings(doc.Items)
	return stored, nil
}

// Update replaces an existing, unexpired conversation snapshot including its
// items. Zero StoredAt or ExpiresAt values preserve the stored retention fields.
func (s *MongoDBStore) Update(ctx context.Context, conversation *StoredConversation) error {
	now := time.Now().UTC()
	normalized, data, _, err := prepareStoredConversationForStorage(conversation, now, s.ttl, false)
	if err != nil {
		return err
	}
	set := bson.M{
		"data":  string(data),
		"items": itemsToStrings(normalized.Items),
	}
	if !normalized.StoredAt.IsZero() {
		set["stored_at"] = normalized.StoredAt.Unix()
	}
	if !normalized.ExpiresAt.IsZero() {
		set["expires_at"] = normalized.ExpiresAt.Unix()
	}
	result, err := s.collection.UpdateOne(ctx, storage.MongoUnexpiredFilter(normalized.Conversation.ID, now), bson.M{"$set": set})
	if err != nil {
		return fmt.Errorf("update conversation snapshot: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// AppendItems atomically appends items to an existing, unexpired conversation
// via $push, so two concurrently completing turns cannot overwrite each
// other's exchange.
func (s *MongoDBStore) AppendItems(ctx context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}
	update := bson.M{"$push": bson.M{"items": bson.M{"$each": itemsToStrings(items)}}}
	result, err := s.collection.UpdateOne(ctx, storage.MongoUnexpiredFilter(id, time.Now()), update)
	if err != nil {
		return fmt.Errorf("append conversation items: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one unexpired conversation snapshot by id.
func (s *MongoDBStore) Delete(ctx context.Context, id string) error {
	result, err := s.collection.DeleteOne(ctx, storage.MongoUnexpiredFilter(id, time.Now()))
	if err != nil {
		return fmt.Errorf("delete conversation snapshot: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired conversation snapshots.
func (s *MongoDBStore) DeleteExpired(ctx context.Context) error {
	filter := bson.M{"expires_at": bson.M{"$gt": 0, "$lte": time.Now().Unix()}}
	if _, err := s.collection.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete expired conversation snapshots: %w", err)
	}
	return nil
}

func itemsToStrings(items []json.RawMessage) []string {
	encoded := make([]string, len(items))
	for i, item := range items {
		encoded[i] = string(item)
	}
	return encoded
}

func itemsFromStrings(items []string) []json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	decoded := make([]json.RawMessage, len(items))
	for i, item := range items {
		decoded[i] = json.RawMessage(item)
	}
	return decoded
}

func (s *MongoDBStore) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.DeleteExpired(ctx); err != nil {
		slog.Warn("conversation snapshot cleanup failed", "error", err)
	}
}

// Close stops the cleanup loop; client lifecycle is managed by the storage layer.
func (s *MongoDBStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCleanup)
	})
	return nil
}
