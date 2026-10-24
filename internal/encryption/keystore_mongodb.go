package encryption

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoKeyDocument struct {
	ID         string    `bson:"_id"`
	WrappedKey []byte    `bson:"wrapped_key"`
	WrapperID  string    `bson:"wrapper_id"`
	KDF        string    `bson:"kdf,omitempty"`
	KDFParams  string    `bson:"kdf_params,omitempty"`
	KDFSalt    []byte    `bson:"kdf_salt,omitempty"`
	Active     bool      `bson:"active"`
	CreatedAt  time.Time `bson:"created_at"`
}

// MongoDBKeyStore stores wrapped data keys in MongoDB.
type MongoDBKeyStore struct {
	collection *mongo.Collection
}

// NewMongoDBKeyStore uses the encryption_keys collection. It needs no
// indexes: the collection holds a handful of documents keyed by _id.
func NewMongoDBKeyStore(_ context.Context, database *mongo.Database) (*MongoDBKeyStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBKeyStore{collection: database.Collection("encryption_keys")}, nil
}

func (s *MongoDBKeyStore) List(ctx context.Context) ([]Key, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list encryption keys: %w", err)
	}
	defer cursor.Close(ctx)
	var keys []Key
	for cursor.Next(ctx) {
		var doc mongoKeyDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode encryption key: %w", err)
		}
		keys = append(keys, Key{
			ID:        doc.ID,
			Wrapped:   doc.WrappedKey,
			WrapperID: doc.WrapperID,
			KDF:       doc.KDF,
			KDFParams: doc.KDFParams,
			Salt:      doc.KDFSalt,
			Active:    doc.Active,
			CreatedAt: doc.CreatedAt.UTC(),
		})
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("list encryption keys: %w", err)
	}
	return keys, nil
}

func (s *MongoDBKeyStore) Insert(ctx context.Context, key Key) (bool, error) {
	_, err := s.collection.InsertOne(ctx, mongoKeyDocument{
		ID:         key.ID,
		WrappedKey: key.Wrapped,
		WrapperID:  key.WrapperID,
		KDF:        key.KDF,
		KDFParams:  key.KDFParams,
		KDFSalt:    key.Salt,
		Active:     key.Active,
		CreatedAt:  key.CreatedAt.UTC(),
	})
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert encryption key: %w", err)
	}
	return true, nil
}

func (s *MongoDBKeyStore) UpdateWrapping(ctx context.Context, key Key) error {
	result, err := s.collection.UpdateOne(ctx, bson.M{"_id": key.ID}, bson.M{"$set": bson.M{
		"wrapped_key": key.Wrapped,
		"wrapper_id":  key.WrapperID,
		"kdf":         key.KDF,
		"kdf_params":  key.KDFParams,
		"kdf_salt":    key.Salt,
	}})
	if err != nil {
		return fmt.Errorf("update encryption key: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("update encryption key: key %q not found", key.ID)
	}
	return nil
}

// Activate sets the new key active, then clears the flag of every older key.
// MongoDB transactions need a replica set, which a standalone server lacks, so
// the two steps are not atomic. Clearing only lower ids keeps concurrent
// rotations from clearing each other's key: whatever interleaving, the
// newest activated key stays active, and readers pick the newest of several.
func (s *MongoDBKeyStore) Activate(ctx context.Context, id string) error {
	result, err := s.collection.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"active": true}})
	if err != nil {
		return fmt.Errorf("activate encryption key: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("activate encryption key: key %q not found", id)
	}
	keys, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("activate encryption key: %w", err)
	}
	older := bson.A{}
	for _, key := range keys {
		if key.Active && keyNumber(key.ID) < keyNumber(id) {
			older = append(older, key.ID)
		}
	}
	if len(older) == 0 {
		return nil
	}
	if _, err := s.collection.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": older}}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		return fmt.Errorf("activate encryption key: %w", err)
	}
	return nil
}
