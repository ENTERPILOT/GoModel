package sharedstore

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
)

// mongoBackend keeps entries in the extension_shared_entries collection.
type mongoBackend struct{ entries *mongo.Collection }

// mongoEntry's _id joins namespace and key with ':', which a valid namespace
// cannot contain, so distinct pairs never share an _id.
type mongoEntry struct {
	ID        string `bson:"_id"`
	Namespace string `bson:"namespace"`
	Key       string `bson:"key"`
	Value     []byte `bson:"value"`
	ExpiresAt int64  `bson:"expires_at"`
	UpdatedAt int64  `bson:"updated_at"`
}

func newMongoBackend(ctx context.Context, database *mongo.Database) (*mongoBackend, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	entries := database.Collection("extension_shared_entries")
	_, err := entries.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "namespace", Value: 1}}},
		{Keys: bson.D{{Key: "expires_at", Value: 1}}},
	})
	if err != nil {
		return nil, fmt.Errorf("create extension_shared_entries indexes: %w", err)
	}
	return &mongoBackend{entries: entries}, nil
}

func mongoID(namespace, key string) string { return namespace + ":" + key }

func mongoLive(now int64) bson.A {
	return bson.A{
		bson.M{"expires_at": 0},
		bson.M{"expires_at": bson.M{"$gt": now}},
	}
}

func (b *mongoBackend) put(ctx context.Context, namespace, key string, value []byte, expiresAt, now int64) error {
	doc := mongoEntry{
		ID: mongoID(namespace, key), Namespace: namespace, Key: key,
		Value: value, ExpiresAt: expiresAt, UpdatedAt: now,
	}
	_, err := b.entries.ReplaceOne(ctx, bson.M{"_id": doc.ID}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("put shared entry %s/%q: %w", namespace, key, err)
	}
	return nil
}

func (b *mongoBackend) get(ctx context.Context, namespace, key string, now int64) ([]byte, bool, error) {
	var doc mongoEntry
	err := b.entries.FindOne(ctx, bson.M{"_id": mongoID(namespace, key), "$or": mongoLive(now)}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get shared entry %s/%q: %w", namespace, key, err)
	}
	return nonNil(doc.Value), true, nil
}

func (b *mongoBackend) remove(ctx context.Context, namespace, key string) error {
	if _, err := b.entries.DeleteOne(ctx, bson.M{"_id": mongoID(namespace, key)}); err != nil {
		return fmt.Errorf("delete shared entry %s/%q: %w", namespace, key, err)
	}
	return nil
}

func (b *mongoBackend) list(ctx context.Context, namespace string, now int64) ([]ext.SharedEntry, error) {
	cursor, err := b.entries.Find(ctx, bson.M{"namespace": namespace, "$or": mongoLive(now)})
	if err != nil {
		return nil, fmt.Errorf("list shared namespace %s: %w", namespace, err)
	}
	var docs []mongoEntry
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("list shared namespace %s: %w", namespace, err)
	}
	entries := make([]ext.SharedEntry, 0, len(docs))
	for _, doc := range docs {
		entries = append(entries, ext.SharedEntry{
			Key: doc.Key, Value: nonNil(doc.Value), ExpiresAt: storage.UnixTime(doc.ExpiresAt),
		})
	}
	return entries, nil
}

func (b *mongoBackend) deleteExpired(ctx context.Context, now int64) error {
	_, err := b.entries.DeleteMany(ctx, bson.M{"expires_at": bson.M{"$gt": 0, "$lte": now}})
	if err != nil {
		return fmt.Errorf("delete expired shared entries: %w", err)
	}
	return nil
}
