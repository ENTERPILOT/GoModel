package conversationstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/core"
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

const (
	mongoMutationMaxAttempts    = 64
	mongoMutationInitialBackoff = time.Millisecond
	mongoMutationMaxBackoff     = 20 * time.Millisecond
)

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

// MergeMetadata uses an optimistic compare-and-swap on the serialized
// snapshot. Items live in a separate field, so they are never rewritten.
func (s *MongoDBStore) MergeMetadata(ctx context.Context, id string, metadata map[string]string) (*StoredConversation, error) {
	for attempt := range mongoMutationMaxAttempts {
		var doc mongoConversationDocument
		// id is encoded by the driver as a BSON string value; it cannot add
		// query keys or operators. lgtm[go/sql-injection]
		if err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("query conversation snapshot: %w", err)
		}
		now := time.Now().UTC()
		if doc.ExpiresAt > 0 && doc.ExpiresAt <= now.Unix() {
			return nil, ErrNotFound
		}
		stored, err := decodeStoredConversation([]byte(doc.Data), nil, doc.StoredAt, doc.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if stored.Conversation.Metadata == nil {
			stored.Conversation.Metadata = make(map[string]string, len(metadata))
		}
		maps.Copy(stored.Conversation.Metadata, metadata)
		if len(stored.Conversation.Metadata) > core.MaxConversationMetadataPairs {
			return nil, ErrMetadataLimitExceeded
		}
		_, data, _, err := prepareStoredConversationForStorage(stored, now, s.ttl, false)
		if err != nil {
			return nil, err
		}
		filter := storage.MongoUnexpiredFilter(id, now)
		filter["data"] = doc.Data
		// Filter/update keys and operators are fixed above; all variable data is
		// encoded as BSON scalar/array values. lgtm[go/sql-injection]
		result, err := s.collection.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"data": string(data)}})
		if err != nil {
			return nil, fmt.Errorf("merge conversation metadata: %w", err)
		}
		if result.MatchedCount == 1 {
			return s.Get(ctx, id)
		}
		if err := waitForMongoMutationRetry(ctx, attempt); err != nil {
			return nil, fmt.Errorf("merge conversation metadata: %w", err)
		}
	}
	return nil, fmt.Errorf("merge conversation metadata: concurrent updates did not settle")
}

// AppendItems atomically appends items to an existing, unexpired conversation.
// Items are stored as JSON strings, so an optimistic compare-and-swap keeps id
// uniqueness and the append in the same atomic operation.
func (s *MongoDBStore) AppendItems(ctx context.Context, id string, items []json.RawMessage) error {
	if len(items) == 0 {
		return nil
	}
	if duplicateItemID(nil, items) != "" {
		return ErrDuplicateItem
	}
	for attempt := range mongoMutationMaxAttempts {
		var doc mongoConversationDocument
		// id is encoded by the driver as a BSON string value; it cannot add
		// query keys or operators. lgtm[go/sql-injection]
		if err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return ErrNotFound
			}
			return fmt.Errorf("query conversation snapshot: %w", err)
		}
		now := time.Now()
		if doc.ExpiresAt > 0 && doc.ExpiresAt <= now.Unix() {
			return ErrNotFound
		}
		if duplicateItemID(itemsFromStrings(doc.Items), items) != "" {
			return ErrDuplicateItem
		}
		filter := storage.MongoUnexpiredFilter(id, now)
		filter["items"] = doc.Items
		update := bson.M{"$push": bson.M{"items": bson.M{"$each": itemsToStrings(items)}}}
		// Filter/update keys and operators are fixed above; all variable data is
		// encoded as BSON scalar/array values. lgtm[go/sql-injection]
		result, err := s.collection.UpdateOne(ctx, filter, update)
		if err != nil {
			return fmt.Errorf("append conversation items: %w", err)
		}
		if result.MatchedCount == 1 {
			return nil
		}
		if err := waitForMongoMutationRetry(ctx, attempt); err != nil {
			return fmt.Errorf("append conversation items: %w", err)
		}
	}
	return fmt.Errorf("append conversation items: concurrent updates did not settle")
}

// DeleteItem uses an optimistic compare-and-swap because MongoDB stores each
// raw item as a JSON string to preserve its exact shape.
func (s *MongoDBStore) DeleteItem(ctx context.Context, id, targetItemID string) (*StoredConversation, error) {
	for attempt := range mongoMutationMaxAttempts {
		var doc mongoConversationDocument
		// id is encoded by the driver as a BSON string value; it cannot add
		// query keys or operators. lgtm[go/sql-injection]
		if err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("query conversation snapshot: %w", err)
		}
		now := time.Now()
		if doc.ExpiresAt > 0 && doc.ExpiresAt <= now.Unix() {
			return nil, ErrNotFound
		}
		index := -1
		for i, raw := range doc.Items {
			if itemID(json.RawMessage(raw)) == targetItemID {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, ErrItemNotFound
		}
		updatedItems := append([]string(nil), doc.Items[:index]...)
		updatedItems = append(updatedItems, doc.Items[index+1:]...)
		filter := storage.MongoUnexpiredFilter(id, now)
		filter["items"] = doc.Items
		// Filter/update keys and operators are fixed above; all variable data is
		// encoded as BSON scalar/array values. lgtm[go/sql-injection]
		result, err := s.collection.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"items": updatedItems}})
		if err != nil {
			return nil, fmt.Errorf("delete conversation item: %w", err)
		}
		if result.MatchedCount == 1 {
			return s.Get(ctx, id)
		}
		if err := waitForMongoMutationRetry(ctx, attempt); err != nil {
			return nil, fmt.Errorf("delete conversation item: %w", err)
		}
	}
	return nil, fmt.Errorf("delete conversation item: concurrent updates did not settle")
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

func waitForMongoMutationRetry(ctx context.Context, attempt int) error {
	if attempt >= mongoMutationMaxAttempts-1 {
		return nil
	}
	shift := min(attempt, 5)
	delay := min(mongoMutationInitialBackoff<<shift, mongoMutationMaxBackoff)
	// Half of the delay is fixed and half randomized. Writers that collide on
	// one snapshot therefore stop retrying in lockstep under heavy contention.
	delay = delay/2 + time.Duration(rand.Int64N(max(int64(delay/2), 1)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
