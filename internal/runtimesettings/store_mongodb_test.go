package runtimesettings

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestMongoDBStoreRoundTrip(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, database *mongo.Database) {
		ctx := context.Background()
		store, err := NewMongoDBStore(ctx, database)
		if err != nil {
			t.Fatalf("create MongoDB store: %v", err)
		}
		if _, found, err := store.Get(ctx, "pro.compression.level"); err != nil || found {
			t.Fatalf("missing Get found=%v err=%v", found, err)
		}
		if err := store.Set(ctx, "pro.compression.level", "high"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		value, found, err := store.Get(ctx, "pro.compression.level")
		if err != nil || !found || value != "high" {
			t.Fatalf("Get value=%q found=%v err=%v", value, found, err)
		}
	})
}
