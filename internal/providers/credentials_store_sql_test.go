package providers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLCredentialStoreTest(t *testing.T, body func(t *testing.T, store *SQLCredentialStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLCredentialStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store, db)
	})
}

// runCredentialStoreSuite exercises behaviour every CredentialStore
// implementation owes its callers, against each backend available in this
// environment.
func runCredentialStoreSuite(t *testing.T, body func(t *testing.T, store CredentialStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLCredentialStore(context.Background(), db)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBCredentialStore(db)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestSQLCredentialStoreReopenKeepsRows(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore, db sqlx.DB) {
		ctx := context.Background()
		sticky := false
		err := store.Upsert(ctx, ManagedProviderCredential{
			Name:              "my-openai",
			Type:              "openai",
			APIKeys:           []string{"sk-one"},
			SessionStickyKeys: &sticky,
			Enabled:           true,
		})
		require.NoError(t, err)

		// Reopening against the same database is what every restart does. It
		// must tolerate the already-applied session_sticky_keys migration and
		// keep rows.
		reopened, err := NewSQLCredentialStore(ctx, db)
		require.NoError(t, err)

		got, err := reopened.Get(ctx, "my-openai")
		require.NoError(t, err)
		require.Equal(t, []string{"sk-one"}, got.APIKeys)
		require.NotNil(t, got.SessionStickyKeys)
		require.False(t, *got.SessionStickyKeys)
	})
}

// A row whose trip_on column holds invalid JSON must surface as an error at
// read time, not silently yield an empty rule set.
func TestSQLCredentialStoreCorruptTripOnRowReturnsError(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore, db sqlx.DB) {
		ctx := context.Background()
		err := store.Upsert(ctx, ManagedProviderCredential{
			Name:    "my-openai",
			Type:    "openai",
			APIKeys: []string{"sk-one"},
			Enabled: true,
		})
		require.NoError(t, err)

		_, err = db.Exec(ctx, `UPDATE provider_credentials SET trip_on = '{' WHERE name = 'my-openai'`)
		require.NoError(t, err)

		_, err = store.List(ctx)
		require.Error(t, err)
	})
}
