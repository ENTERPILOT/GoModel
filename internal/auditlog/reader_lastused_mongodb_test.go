package auditlog

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/stretchr/testify/require"
)

func TestMongoReader_GetLastUsedByAuthKeys_Errors(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		reader, err := NewMongoDBReader(db)
		require.NoError(t, err)
		collection := db.Collection("audit_logs")

		t.Run("aggregate error", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := reader.GetLastUsedByAuthKeys(ctx, []string{"key-1"})
			require.ErrorIs(t, err, context.Canceled)
		})

		t.Run("decode error", func(t *testing.T) {
			// A timestamp stored as a string cannot decode into time.Time.
			_, err := collection.InsertOne(context.Background(), map[string]any{
				"auth_key_id": "key-1",
				"timestamp":   "not-a-date",
				"created_at":  time.Now().UTC(),
			})
			require.NoError(t, err)
			_, err = reader.GetLastUsedByAuthKeys(context.Background(), []string{"key-1"})
			require.ErrorContains(t, err, "error iterating auth key last used cursor")
		})
	})
}
