package authkeys

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoAuthKeyDocument struct {
	ID            string     `bson:"_id"`
	Name          string     `bson:"name"`
	Description   string     `bson:"description,omitempty"`
	RedactedValue string     `bson:"redacted_value"`
	SecretHash    string     `bson:"secret_hash"`
	Enabled       bool       `bson:"enabled"`
	ExpiresAt     *time.Time `bson:"expires_at,omitempty"`
	DeactivatedAt *time.Time `bson:"deactivated_at,omitempty"`
	CreatedAt     time.Time  `bson:"created_at"`
	UpdatedAt     time.Time  `bson:"updated_at"`
}

type mongoAuthKeyIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores auth keys in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	coll := database.Collection("auth_keys")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "secret_hash", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "enabled", Value: 1}}},
		{Keys: bson.D{{Key: "created_at", Value: -1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create auth_keys indexes: %w", err)
	}
	return &MongoDBStore{collection: coll}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]AuthKey, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list auth keys: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]AuthKey, 0)
	for cursor.Next(ctx) {
		var doc mongoAuthKeyDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode auth key: %w", err)
		}
		result = append(result, authKeyFromMongo(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate auth keys: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Create(ctx context.Context, key AuthKey) error {
	_, err := s.collection.InsertOne(ctx, mongoAuthKeyDocument{
		ID:            key.ID,
		Name:          key.Name,
		Description:   key.Description,
		RedactedValue: key.RedactedValue,
		SecretHash:    key.SecretHash,
		Enabled:       key.Enabled,
		ExpiresAt:     key.ExpiresAt,
		DeactivatedAt: key.DeactivatedAt,
		CreatedAt:     key.CreatedAt.UTC(),
		UpdatedAt:     key.UpdatedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("create auth key: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Deactivate(ctx context.Context, id string, now time.Time) error {
	result, err := s.collection.UpdateOne(ctx, mongoAuthKeyIDFilter{ID: normalizeID(id)}, bson.M{
		"$set": bson.M{
			"enabled":    false,
			"updated_at": now.UTC(),
		},
		"$setOnInsert": bson.M{},
		"$max": bson.M{
			"deactivated_at": now.UTC(),
		},
	})
	if err != nil {
		return fmt.Errorf("deactivate auth key: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}

func authKeyFromMongo(doc mongoAuthKeyDocument) AuthKey {
	return AuthKey{
		ID:            doc.ID,
		Name:          doc.Name,
		Description:   doc.Description,
		RedactedValue: doc.RedactedValue,
		SecretHash:    doc.SecretHash,
		Enabled:       doc.Enabled,
		ExpiresAt:     timePtrUTC(doc.ExpiresAt),
		DeactivatedAt: timePtrUTC(doc.DeactivatedAt),
		CreatedAt:     doc.CreatedAt.UTC(),
		UpdatedAt:     doc.UpdatedAt.UTC(),
	}
}

func timePtrUTC(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	t := value.UTC()
	return &t
}
