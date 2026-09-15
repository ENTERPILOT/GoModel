package runtimesettings

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestMongoDBStoreRoundTrip(t *testing.T) {
	_, err := NewMongoDBStore(context.Background(), nil)
	require.Error(t, err)

	mongotest.Run(t, func(t *testing.T, database *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(ctx, database)
		require.NoError(t, err)
		_, found, err := store.Get(ctx, "pro.compression.level")
		require.NoError(t, err)
		require.False(t, found)
		err = store.Set(ctx, "pro.compression.level", "high")
		require.NoError(t, err)

		value, found, err := store.Get(ctx, "pro.compression.level")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "high", value)
		stored, err := store.SetDefault(ctx, "pro.compression.level", "low")
		require.NoError(t, err)
		require.Equal(t, "high", stored)
		stored, err = store.SetDefault(ctx, "install_id", "first")
		require.NoError(t, err)
		require.Equal(t, "first", stored)

	})
}
