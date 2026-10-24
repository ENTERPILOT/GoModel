package auditlog

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/stretchr/testify/require"
)

func TestNewMongoDBStoreDropsLegacyExecutionPlanIndex(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		coll := db.Collection("audit_logs")
		// A collection from before v0.1.17 still carries the pre-rename index.
		_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "execution_plan_version_id", Value: 1}}})
		require.NoError(t, err)
		_, err = NewMongoDBStore(db, 0)
		require.NoError(t, err)

		cursor, err := coll.Indexes().List(ctx)
		require.NoError(t, err)

		var specs []bson.M
		err = cursor.All(ctx, &specs)
		require.NoError(t, err)

		for _, spec := range specs {
			require.NotEqual(t, legacyExecutionPlanIndex, spec["name"], "legacy index still present after NewMongoDBStore: %v", specs)
		}
		// A second start finds no legacy index; that is not an error either.
		_, err = NewMongoDBStore(db, 0)
		require.NoError(t, err)
	})
}

func TestIsIndexNotFound(t *testing.T) {
	require.True(t, isIndexNotFound(mongo.CommandError{Code: 27, Name: "IndexNotFound"}))
	require.False(t, isIndexNotFound(mongo.CommandError{Code: 26, Name: "NamespaceNotFound"}))
	require.False(t, isIndexNotFound(errors.New("connection reset")))
}
