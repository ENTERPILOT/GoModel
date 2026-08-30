package users

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoUserDocument struct {
	ID            string    `bson:"_id"`
	AllowedModels []string  `bson:"allowed_models"`
	Description   string    `bson:"description,omitempty"`
	CreatedAt     time.Time `bson:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at"`
}

type mongoUserIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores user policies in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore binds the users collection.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBStore{collection: database.Collection("users")}, nil
}

func (s *MongoDBStore) List(ctx context.Context) ([]User, error) {
	cursor, err := s.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer cursor.Close(ctx)

	result := make([]User, 0)
	for cursor.Next(ctx) {
		var doc mongoUserDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode user: %w", err)
		}
		user := User{
			UserPath:    doc.ID,
			Description: doc.Description,
			CreatedAt:   doc.CreatedAt.UTC(),
			UpdatedAt:   doc.UpdatedAt.UTC(),
		}
		if len(doc.AllowedModels) > 0 {
			user.AllowedModels = doc.AllowedModels
		}
		result = append(result, user)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return result, nil
}

func (s *MongoDBStore) Upsert(ctx context.Context, user User) error {
	allowed := user.AllowedModels
	if allowed == nil {
		allowed = []string{}
	}
	update := bson.M{
		"$set": bson.M{
			"allowed_models": allowed,
			"description":    user.Description,
			"updated_at":     user.UpdatedAt.UTC(),
		},
		"$setOnInsert": bson.M{
			"created_at": user.CreatedAt.UTC(),
		},
	}
	_, err := s.collection.UpdateOne(ctx, mongoUserIDFilter{ID: user.UserPath}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

func (s *MongoDBStore) Delete(ctx context.Context, userPath string) error {
	result, err := s.collection.DeleteOne(ctx, mongoUserIDFilter{ID: userPath})
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}
