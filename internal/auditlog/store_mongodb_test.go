package auditlog

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
)

func TestNewMongoDBStoreDropsLegacyExecutionPlanIndex(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		coll := db.Collection("audit_logs")
		// A collection from before v0.1.17 still carries the pre-rename index.
		if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "execution_plan_version_id", Value: 1}}}); err != nil {
			t.Fatalf("create legacy index: %v", err)
		}

		if _, err := NewMongoDBStore(db, 0); err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}

		cursor, err := coll.Indexes().List(ctx)
		if err != nil {
			t.Fatalf("list indexes: %v", err)
		}
		var specs []bson.M
		if err := cursor.All(ctx, &specs); err != nil {
			t.Fatalf("decode indexes: %v", err)
		}
		for _, spec := range specs {
			if spec["name"] == legacyExecutionPlanIndex {
				t.Fatalf("legacy index still present after NewMongoDBStore: %v", specs)
			}
		}

		// A second start finds no legacy index; that is not an error either.
		if _, err := NewMongoDBStore(db, 0); err != nil {
			t.Fatalf("NewMongoDBStore (second start): %v", err)
		}
	})
}

func TestIsIndexNotFound(t *testing.T) {
	if !isIndexNotFound(mongo.CommandError{Code: 27, Name: "IndexNotFound"}) {
		t.Fatal("code 27 should be reported as index not found")
	}
	if isIndexNotFound(mongo.CommandError{Code: 26, Name: "NamespaceNotFound"}) {
		t.Fatal("other command errors must not be treated as index not found")
	}
	if isIndexNotFound(errors.New("connection reset")) {
		t.Fatal("non-command errors must not be treated as index not found")
	}
}
