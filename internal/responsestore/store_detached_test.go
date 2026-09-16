package responsestore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestDetachRequiresResponseID(t *testing.T) {
	for _, src := range []*StoredResponse{
		nil,
		{},
		{Response: &core.ResponsesResponse{}},
	} {
		_, err := Detach(src)
		require.Error(t, err)
	}
}

func TestDetachSharesNoMemoryWithSource(t *testing.T) {
	src := testStoredResponse("resp-detached")
	snapshot, err := Detach(src)
	require.NoError(t, err)
	require.Equal(t, "resp-detached", snapshot.ID())

	// Mutate the source after detaching; the persisted snapshot must keep the
	// pre-mutation state.
	src.Response.Model = "gpt-mutated"
	src.InputItems[0] = []byte(`{"mutated":true}`)

	store := NewMemoryStore(WithUnboundedRetention())
	t.Cleanup(func() { _ = store.Close() })
	err = snapshot.Persist(context.Background(), store)
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "resp-detached")
	require.NoError(t, err)
	require.Equal(t, "gpt-test", got.Response.Model)
	require.NotEqual(t, `{"mutated":true}`, string(got.InputItems[0]))
}

// TestDetachedPersistSuite exercises Persist against every Store backend:
// first write creates, second write upserts, retention is stamped.
func TestDetachedPersistSuite(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		first, err := Detach(testStoredResponse("resp-persist"))
		require.NoError(t, err)
		err = first.Persist(ctx, store)
		require.NoError(t, err)

		got, err := store.Get(ctx, "resp-persist")
		require.NoError(t, err)
		require.NotNil(t, got.Response)
		require.Equal(t, "gpt-test", got.Response.Model)
		require.Equal(t, "openai", got.Provider)
		require.Equal(t, "req-1", got.RequestID, "metadata = %+v, want provider and request id preserved", got)
		require.False(t, got.StoredAt.IsZero())

		// A second Persist for the same id must overwrite the live row via the
		// update fallback, not fail as a duplicate.
		updatedSrc := testStoredResponse("resp-persist")
		updatedSrc.Response.Model = "gpt-updated"
		second, err := Detach(updatedSrc)
		require.NoError(t, err)
		err = second.Persist(ctx, store)
		require.NoError(t, err)

		got, err = store.Get(ctx, "resp-persist")
		require.NoError(t, err)
		require.Equal(t, "gpt-updated", got.Response.Model)
	})
}

func TestDetachNormalizesMetadata(t *testing.T) {
	src := testStoredResponse("resp-normalize")
	src.Provider = "  openai  "
	src.RequestID = " req-9 "
	src.ProviderResponseID = ""

	snapshot, err := Detach(src)
	require.NoError(t, err)

	store := NewMemoryStore(WithUnboundedRetention())
	t.Cleanup(func() { _ = store.Close() })
	err = snapshot.Persist(context.Background(), store)
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "resp-normalize")
	require.NoError(t, err)
	require.Equal(t, "openai", got.Provider)
	require.Equal(t, "req-9", got.RequestID)
	require.Equal(t, "resp-normalize", got.ProviderResponseID)
}

// erroringStore fails Create and Update with distinct errors, exercising the
// regular-store fallback failure path.
type erroringStore struct {
	Store
	createErr error
	updateErr error
}

func (s *erroringStore) Create(context.Context, *StoredResponse) error { return s.createErr }
func (s *erroringStore) Update(context.Context, *StoredResponse) error { return s.updateErr }

// erroringSerializedStore fails both serialized write paths with distinct
// errors.
type erroringSerializedStore struct {
	Store
	createErr error
	updateErr error
}

func (s *erroringSerializedStore) createSerialized(context.Context, string, []byte, time.Time, time.Time) error {
	return s.createErr
}

func (s *erroringSerializedStore) updateSerialized(context.Context, string, []byte, time.Time, time.Time) error {
	return s.updateErr
}

func TestDetachedPersistJoinsCreateAndUpdateErrors(t *testing.T) {
	createErr := errors.New("create boom")
	updateErr := errors.New("update boom")
	snapshot, err := Detach(testStoredResponse("resp-fail"))
	require.NoError(t, err)

	for name, store := range map[string]Store{
		"regular":    &erroringStore{createErr: createErr, updateErr: updateErr},
		"serialized": &erroringSerializedStore{createErr: createErr, updateErr: updateErr},
	} {
		err := snapshot.Persist(context.Background(), store)
		require.Error(t, err, "store %q", name)
		require.ErrorIs(t, err, createErr)
		require.ErrorIs(t, err, updateErr)
	}
}

func TestSQLStorePersistPreservesExplicitRetention(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		src := testStoredResponse("resp-explicit")
		src.StoredAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		src.ExpiresAt = time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
		snapshot, err := Detach(src)
		require.NoError(t, err)
		err = snapshot.Persist(ctx, store)
		require.NoError(t, err)

		got, err := store.Get(ctx, "resp-explicit")
		require.NoError(t, err)
		require.Equal(t, src.StoredAt.Unix(), got.StoredAt.Unix())
		require.Equal(t, src.ExpiresAt.Unix(), got.ExpiresAt.Unix(), "retention = (%v, %v), want explicit (%v, %v)", got.StoredAt, got.ExpiresAt, src.StoredAt, src.ExpiresAt)

		// A same-id persist with different explicit retention replaces both
		// columns through the update fallback.
		replaced := testStoredResponse("resp-explicit")
		replaced.StoredAt = time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
		replaced.ExpiresAt = time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
		replacedSnapshot, err := Detach(replaced)
		require.NoError(t, err)
		err = replacedSnapshot.Persist(ctx, store)
		require.NoError(t, err)

		got, err = store.Get(ctx, "resp-explicit")
		require.NoError(t, err)
		require.Equal(t, replaced.StoredAt.Unix(), got.StoredAt.Unix())
		require.Equal(t, replaced.ExpiresAt.Unix(), got.ExpiresAt.Unix(), "retention = (%v, %v), want replaced (%v, %v)", got.StoredAt, got.ExpiresAt, replaced.StoredAt, replaced.ExpiresAt)

		// An already-expired snapshot is silently skipped, mirroring Create.
		expired := testStoredResponse("resp-expired")
		expired.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		expiredSnapshot, err := Detach(expired)
		require.NoError(t, err)
		err = expiredSnapshot.Persist(ctx, store)
		require.NoError(t, err)
		_, err = store.Get(ctx, "resp-expired")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSQLStorePersistStampsRetentionColumns(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		snapshot, err := Detach(testStoredResponse("resp-retention"))
		require.NoError(t, err)

		before := time.Now().UTC().Add(-time.Second)
		err = snapshot.Persist(ctx, store)
		require.NoError(t, err)

		got, err := store.Get(ctx, "resp-retention")
		require.NoError(t, err)
		require.False(t, got.StoredAt.Before(before), "StoredAt = %v, want stamped at write time", got.StoredAt)
		require.True(t, got.ExpiresAt.After(got.StoredAt), "ExpiresAt = %v, want after StoredAt %v", got.ExpiresAt, got.StoredAt)

		// The update fallback must preserve the original retention columns.
		storedAt, expiresAt := got.StoredAt, got.ExpiresAt
		updated, err := Detach(testStoredResponse("resp-retention"))
		require.NoError(t, err)
		err = updated.Persist(ctx, store)
		require.NoError(t, err)

		got, err = store.Get(ctx, "resp-retention")
		require.NoError(t, err)
		require.True(t, got.StoredAt.Equal(storedAt))
		require.True(t, got.ExpiresAt.Equal(expiresAt), "retention = (%v, %v), want preserved (%v, %v)", got.StoredAt, got.ExpiresAt, storedAt, expiresAt)
	})
}
