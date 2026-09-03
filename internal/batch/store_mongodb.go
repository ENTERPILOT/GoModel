package batch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoBatchDocument struct {
	ID        string `bson:"_id"`
	CreatedAt int64  `bson:"created_at"`
	UpdatedAt int64  `bson:"updated_at"`
	Status    string `bson:"status"`
	Data      []byte `bson:"data"`
	UserPath  string `bson:"user_path"`
}

// MongoDBStore stores batches in MongoDB.
type MongoDBStore struct {
	collection *mongo.Collection
}

// NewMongoDBStore creates collection indexes if needed.
func NewMongoDBStore(database *mongo.Database) (*MongoDBStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}

	coll := database.Collection("batches")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "user_path", Value: 1}}},
	}
	if _, err := coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return nil, fmt.Errorf("create batches indexes: %w", err)
	}

	store := &MongoDBStore{collection: coll}
	store.backfillUserPath()
	return store, nil
}

// backfillUserPathTimeout bounds the whole start-up pass independently of
// the index deadline; backfillUserPathChunk (store_sql.go) bounds each pass.
const backfillUserPathTimeout = 5 * time.Minute

// backfillUserPath copies the user path out of payloads persisted before the
// field existed, one bounded chunk at a time. Documents still lacking the
// field after a failure are picked up on the next start, so a partial pass
// is safe; it is logged rather than fatal because scoped listings merely
// miss those rows until then.
func (s *MongoDBStore) backfillUserPath() {
	ctx, cancel := context.WithTimeout(context.Background(), backfillUserPathTimeout)
	defer cancel()
	for {
		cursor, err := s.collection.Find(ctx, bson.M{"user_path": bson.M{"$exists": false}}, options.Find().SetLimit(backfillUserPathChunk))
		if err != nil {
			slog.Warn("batch user_path backfill paused", "error", err)
			return
		}
		var docs []mongoBatchDocument
		err = cursor.All(ctx, &docs)
		if err != nil {
			slog.Warn("batch user_path backfill paused", "error", err)
			return
		}
		if len(docs) == 0 {
			return
		}
		for _, doc := range docs {
			userPath := ""
			if batch, err := deserializeBatch(doc.Data); err == nil {
				userPath = batch.UserPath
			}
			if _, err := s.collection.UpdateOne(ctx, bson.M{"_id": doc.ID}, bson.M{"$set": bson.M{"user_path": userPath}}); err != nil {
				slog.Warn("batch user_path backfill paused", "batch_id", doc.ID, "error", err)
				return
			}
		}
		if len(docs) < backfillUserPathChunk {
			return
		}
	}
}

// Create inserts a new batch.
func (s *MongoDBStore) Create(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	doc := mongoBatchDocument{
		ID:        batch.Batch.ID,
		CreatedAt: batch.Batch.CreatedAt,
		UpdatedAt: time.Now().Unix(),
		Status:    batch.Batch.Status,
		Data:      payload,
		UserPath:  strings.TrimSpace(batch.UserPath),
	}
	if _, err := s.collection.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}
	return nil
}

// Get returns a batch by id.
func (s *MongoDBStore) Get(ctx context.Context, id string) (*StoredBatch, error) {
	var doc mongoBatchDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query batch: %w", err)
	}

	batch, err := deserializeBatch(doc.Data)
	if err != nil {
		return nil, fmt.Errorf("decode batch: %w", err)
	}
	return batch, nil
}

// List returns batches ordered by created_at desc, id desc.
func (s *MongoDBStore) List(ctx context.Context, limit int, after, userPath string) ([]*StoredBatch, error) {
	limit = normalizeLimit(limit)
	var conditions bson.A
	switch userPath {
	case "":
	case "/":
		// Root admits every tracked path, never the legacy rows without one.
		conditions = append(conditions, bson.M{"user_path": bson.M{"$exists": true, "$ne": ""}})
	default:
		// Descendants sort in the half-open byte range [path+"/", path+"0"):
		// '0' is the byte after '/', so no regex over caller input is needed.
		conditions = append(conditions, bson.M{"$or": bson.A{
			bson.M{"user_path": userPath},
			bson.M{"user_path": bson.M{"$gte": userPath + "/", "$lt": userPath + "0"}},
		}})
	}

	if after != "" {
		var cursorDoc mongoBatchDocument
		err := s.collection.FindOne(ctx, bson.M{"_id": after}).Decode(&cursorDoc)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("query after cursor: %w", err)
		}
		if userPath != "" && !core.UserPathContains(userPath, cursorDoc.UserPath) {
			// A cursor outside the subtree is indistinguishable from a missing one.
			return nil, ErrNotFound
		}
		conditions = append(conditions, bson.M{
			"$or": bson.A{
				bson.M{"created_at": bson.M{"$lt": cursorDoc.CreatedAt}},
				bson.M{
					"created_at": cursorDoc.CreatedAt,
					"_id":        bson.M{"$lt": cursorDoc.ID},
				},
			},
		})
	}
	filter := bson.M{}
	if len(conditions) > 0 {
		filter = bson.M{"$and": conditions}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).
		SetLimit(int64(limit))
	cursor, err := s.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	defer cursor.Close(ctx)

	items := make([]*StoredBatch, 0, limit)
	for cursor.Next(ctx) {
		var doc mongoBatchDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode batch document: %w", err)
		}
		batch, err := deserializeBatch(doc.Data)
		if err != nil {
			return nil, fmt.Errorf("decode batch payload: %w", err)
		}
		items = append(items, batch)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate batches cursor: %w", err)
	}

	return items, nil
}

// Update updates a stored batch object.
func (s *MongoDBStore) Update(ctx context.Context, batch *StoredBatch) error {
	payload, err := serializeBatch(batch)
	if err != nil {
		return err
	}

	result, err := s.collection.UpdateOne(ctx,
		bson.M{"_id": batch.Batch.ID},
		bson.M{"$set": bson.M{
			"updated_at": time.Now().Unix(),
			"status":     batch.Batch.Status,
			"data":       payload,
			"user_path":  strings.TrimSpace(batch.UserPath),
		}},
	)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a stored batch object.
func (s *MongoDBStore) Delete(ctx context.Context, id string) error {
	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return fmt.Errorf("delete batch: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Close is a no-op; Mongo client lifecycle is managed by storage layer.
func (s *MongoDBStore) Close() error {
	return nil
}
