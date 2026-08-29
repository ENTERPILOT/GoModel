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
	ID          string    `bson:"_id"`
	UserPath    string    `bson:"user_path"`
	Name        string    `bson:"name,omitempty"`
	Description string    `bson:"description,omitempty"`
	Group       string    `bson:"group,omitempty"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

type mongoGroupDocument struct {
	Name        string    `bson:"_id"`
	Description string    `bson:"description,omitempty"`
	Parent      string    `bson:"parent,omitempty"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

type mongoIDFilter struct {
	ID string `bson:"_id"`
}

// MongoDBStore stores users and groups in MongoDB.
type MongoDBStore struct {
	users  *mongo.Collection
	groups *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	users := database.Collection("users")
	groups := database.Collection("user_groups")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_path", Value: 1}}, Options: options.Index().SetUnique(true)},
	}
	if _, err := users.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create users indexes: %w", err)
	}
	return &MongoDBStore{users: users, groups: groups}, nil
}

func (s *MongoDBStore) ListUsers(ctx context.Context) ([]User, error) {
	cursor, err := s.users.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "user_path", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrapStoreErr("list users", err)
	}
	defer cursor.Close(ctx)

	result := make([]User, 0)
	for cursor.Next(ctx) {
		var doc mongoUserDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, wrapStoreErr("decode user", err)
		}
		result = append(result, User{
			ID:          doc.ID,
			UserPath:    doc.UserPath,
			Name:        doc.Name,
			Description: doc.Description,
			Group:       doc.Group,
			CreatedAt:   doc.CreatedAt.UTC(),
			UpdatedAt:   doc.UpdatedAt.UTC(),
		})
	}
	if err := cursor.Err(); err != nil {
		return nil, wrapStoreErr("iterate users", err)
	}
	return result, nil
}

func (s *MongoDBStore) UpsertUser(ctx context.Context, user User) error {
	doc := mongoUserDocument{
		ID:          user.ID,
		UserPath:    user.UserPath,
		Name:        user.Name,
		Description: user.Description,
		Group:       user.Group,
		CreatedAt:   user.CreatedAt.UTC(),
		UpdatedAt:   user.UpdatedAt.UTC(),
	}
	_, err := s.users.ReplaceOne(ctx, mongoIDFilter{ID: user.ID}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return wrapStoreErr("upsert user", err)
	}
	return nil
}

func (s *MongoDBStore) DeleteUser(ctx context.Context, id string) error {
	result, err := s.users.DeleteOne(ctx, mongoIDFilter{ID: normalizeID(id)})
	if err != nil {
		return wrapStoreErr("delete user", err)
	}
	if result.DeletedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *MongoDBStore) ListGroups(ctx context.Context) ([]Group, error) {
	cursor, err := s.groups.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrapStoreErr("list groups", err)
	}
	defer cursor.Close(ctx)

	result := make([]Group, 0)
	for cursor.Next(ctx) {
		var doc mongoGroupDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, wrapStoreErr("decode group", err)
		}
		result = append(result, Group{
			Name:        doc.Name,
			Description: doc.Description,
			Parent:      doc.Parent,
			CreatedAt:   doc.CreatedAt.UTC(),
			UpdatedAt:   doc.UpdatedAt.UTC(),
		})
	}
	if err := cursor.Err(); err != nil {
		return nil, wrapStoreErr("iterate groups", err)
	}
	return result, nil
}

func (s *MongoDBStore) UpsertGroup(ctx context.Context, group Group) error {
	doc := mongoGroupDocument{
		Name:        group.Name,
		Description: group.Description,
		Parent:      group.Parent,
		CreatedAt:   group.CreatedAt.UTC(),
		UpdatedAt:   group.UpdatedAt.UTC(),
	}
	_, err := s.groups.ReplaceOne(ctx, mongoIDFilter{ID: group.Name}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return wrapStoreErr("upsert group", err)
	}
	return nil
}

func (s *MongoDBStore) DeleteGroup(ctx context.Context, name string) error {
	result, err := s.groups.DeleteOne(ctx, mongoIDFilter{ID: name})
	if err != nil {
		return wrapStoreErr("delete group", err)
	}
	if result.DeletedCount == 0 {
		return ErrGroupNotFound
	}
	return nil
}

func (s *MongoDBStore) Close() error {
	return nil
}
