package auditlog

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/stretchr/testify/assert"
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

func TestNewMongoDBStoreReplacesLegacyAuthKeyIndex(t *testing.T) {
	tests := []struct {
		name       string
		conflict   bool
		wantLegacy bool
	}{
		{name: "compound index created, legacy dropped"},
		// When the compound index cannot be created, the legacy index stays.
		{name: "create fails, legacy kept", conflict: true, wantLegacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
				ctx := context.Background()
				coll := db.Collection("audit_logs")
				_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "auth_key_id", Value: 1}}})
				require.NoError(t, err)
				if tt.conflict {
					// Same name as the compound index, different keys.
					_, err = coll.Indexes().CreateOne(ctx, mongo.IndexModel{
						Keys:    bson.D{{Key: "auth_key_id", Value: 1}, {Key: "timestamp", Value: 1}},
						Options: options.Index().SetName("auth_key_id_1_timestamp_-1"),
					})
					require.NoError(t, err)
				}

				_, err = NewMongoDBStore(db, 0)
				require.NoError(t, err)

				cursor, err := coll.Indexes().List(ctx)
				require.NoError(t, err)
				var specs []bson.M
				require.NoError(t, cursor.All(ctx, &specs))
				names := map[string]bool{}
				for _, spec := range specs {
					names[spec["name"].(string)] = true
				}
				assert.Equal(t, tt.wantLegacy, names[legacyAuthKeyIndex], "indexes: %v", specs)
				assert.True(t, names["auth_key_id_1_timestamp_-1"], "indexes: %v", specs)
			})
		})
	}
}
