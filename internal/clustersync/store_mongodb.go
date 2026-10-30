package clustersync

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoDBStore keeps configuration revisions in MongoDB.
type MongoDBStore struct{ revisions *mongo.Collection }

type mongoRevision struct {
	Entity   string `bson:"_id"`
	Revision int64  `bson:"revision"`
}

// NewMongoDBStore uses the shared config_revisions collection.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &MongoDBStore{revisions: database.Collection("config_revisions")}, nil
}

// Bump increments entity's revision. $inc on an upsert keyed by _id is atomic
// on the document, so concurrent bumps never lose an increment.
func (s *MongoDBStore) Bump(ctx context.Context, entity string) error {
	_, err := s.revisions.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: entity}},
		bson.D{
			{Key: "$inc", Value: bson.D{{Key: "revision", Value: int64(1)}}},
			{Key: "$set", Value: bson.D{{Key: "updated_at", Value: time.Now().Unix()}}},
		},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("bump config revision %q: %w", entity, err)
	}
	return nil
}

// Revisions returns every stored revision.
func (s *MongoDBStore) Revisions(ctx context.Context) (map[string]int64, error) {
	cursor, err := s.revisions.Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("list config revisions: %w", err)
	}
	var docs []mongoRevision
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("list config revisions: %w", err)
	}
	revisions := make(map[string]int64, len(docs))
	for _, doc := range docs {
		revisions[doc.Entity] = doc.Revision
	}
	return revisions, nil
}
