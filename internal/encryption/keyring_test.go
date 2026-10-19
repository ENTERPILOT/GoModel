package encryption_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// runKeyStoreSuite runs body against every KeyStore backend available here.
func runKeyStoreSuite(t *testing.T, body func(t *testing.T, store encryption.KeyStore)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { body(t, encryptiontest.NewKeyStore()) })
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := encryption.NewSQLKeyStore(context.Background(), db)
		require.NoError(t, err)
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := encryption.NewMongoDBKeyStore(context.Background(), db)
		require.NoError(t, err)
		body(t, store)
	})
}

// fakeKMS is an extension KeyWrapper backed by a fixed AES key.
type fakeKMS struct {
	id   string
	aead cipher.AEAD
	fail error
}

func newFakeKMS(t *testing.T, id string) *fakeKMS {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return &fakeKMS{id: id, aead: aead}
}

func (k *fakeKMS) ID() string { return k.id }

func (k *fakeKMS) WrapKey(_ context.Context, dek []byte) ([]byte, error) {
	if k.fail != nil {
		return nil, k.fail
	}
	nonce := make([]byte, k.aead.NonceSize())
	return k.aead.Seal(nonce, nonce, dek, []byte(k.id)), nil
}

func (k *fakeKMS) UnwrapKey(_ context.Context, wrapped []byte) ([]byte, error) {
	if k.fail != nil {
		return nil, k.fail
	}
	n := k.aead.NonceSize()
	return k.aead.Open(nil, wrapped[:n], wrapped[n:], []byte(k.id))
}

var testAAD = encryption.AAD("provider_credential", "openai", "api_keys")

func open(t *testing.T, store encryption.KeyStore, opts encryption.Options) *encryption.Box {
	t.Helper()
	box, err := encryption.Open(context.Background(), store, opts)
	require.NoError(t, err)
	return box
}

func seal(t *testing.T, box *encryption.Box, plaintext string) string {
	t.Helper()
	sealed, err := box.Seal(testAAD, plaintext)
	require.NoError(t, err)
	return sealed
}

func assertOpens(t *testing.T, box *encryption.Box, sealed, want string) {
	t.Helper()
	got, err := box.Open(testAAD, sealed)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func onlyKey(t *testing.T, store encryption.KeyStore) encryption.Key {
	t.Helper()
	keys, err := store.List(context.Background())
	require.NoError(t, err)
	require.Len(t, keys, 1)
	return keys[0]
}

func TestOpenWithoutKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		box := open(t, store, encryption.Options{})
		assert.False(t, box.Enabled())
		keys, err := store.List(context.Background())
		require.NoError(t, err)
		assert.Empty(t, keys, "no data key is created without a key")

		open(t, store, encryption.Options{Key: "k1"})
		_, err = encryption.Open(context.Background(), store, encryption.Options{})
		require.Error(t, err, "a database with encrypted secrets must not start without the key")
		assert.Contains(t, err.Error(), "GOMODEL_ENCRYPTION_KEY is not set")
	})
}

func TestOpenCreatesAndReusesDataKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		first := open(t, store, encryption.Options{Key: "k1"})
		require.True(t, first.Enabled())
		assert.Equal(t, "1", first.ActiveKeyID())

		key := onlyKey(t, store)
		assert.Equal(t, "1", key.ID)
		assert.True(t, key.Active)
		assert.Equal(t, encryption.LocalWrapperID, key.WrapperID)
		assert.Equal(t, "argon2id", key.KDF)
		assert.Equal(t, "m=19456,t=2,p=1", key.KDFParams)
		assert.Len(t, key.Salt, 16)
		assert.Len(t, key.Wrapped, 12+32+16, "nonce || wrapped DEK || tag")
		assert.WithinDuration(t, time.Now(), key.CreatedAt, time.Minute)

		sealed := seal(t, first, "sk-one")
		restarted := open(t, store, encryption.Options{Key: "k1"})
		assertOpens(t, restarted, sealed, "sk-one")
		assert.Len(t, onlyKeys(t, store), 1, "a restart reuses the data key")
	})
}

func onlyKeys(t *testing.T, store encryption.KeyStore) []encryption.Key {
	t.Helper()
	keys, err := store.List(context.Background())
	require.NoError(t, err)
	return keys
}

func TestOpenWithWrongKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		open(t, store, encryption.Options{Key: "k1"})
		_, err := encryption.Open(context.Background(), store, encryption.Options{Key: "wrong"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "GOMODEL_ENCRYPTION_KEY does not decrypt data key")
		assert.Contains(t, err.Error(), "GOMODEL_ENCRYPTION_KEY_PREVIOUS")
		assert.NotContains(t, err.Error(), "wrong")

		_, err = encryption.Open(context.Background(), store, encryption.Options{Key: "wrong", PreviousKey: "also-wrong"})
		require.Error(t, err)
	})
}

func TestOpenRotatesKEKWithPreviousKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		sealed := seal(t, open(t, store, encryption.Options{Key: "old"}), "sk-one")
		before := onlyKey(t, store)

		rotated := open(t, store, encryption.Options{Key: "new", PreviousKey: "old"})
		assertOpens(t, rotated, sealed, "sk-one")
		after := onlyKey(t, store)
		assert.Equal(t, encryption.LocalWrapperID, after.WrapperID)
		assert.NotEqual(t, before.Salt, after.Salt, "the data key is re-wrapped under a fresh salt")
		assert.NotEqual(t, before.Wrapped, after.Wrapped)

		assertOpens(t, open(t, store, encryption.Options{Key: "new"}), sealed, "sk-one")
		_, err := encryption.Open(context.Background(), store, encryption.Options{Key: "old"})
		require.Error(t, err, "the old key no longer unwraps the data key")
	})
}

func TestOpenMovesDataKeyToExtensionWrapper(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		ctx := context.Background()
		sealed := seal(t, open(t, store, encryption.Options{Key: "local"}), "sk-one")
		kms := newFakeKMS(t, "kms:test")

		_, err := encryption.Open(ctx, store, encryption.Options{Wrapper: kms})
		require.Error(t, err, "a locally wrapped key needs the local key once to move")
		assert.Contains(t, err.Error(), "wrapped with GOMODEL_ENCRYPTION_KEY")

		moved := open(t, store, encryption.Options{Wrapper: kms, Key: "local"})
		assertOpens(t, moved, sealed, "sk-one")
		key := onlyKey(t, store)
		assert.Equal(t, "kms:test", key.WrapperID)
		assert.Empty(t, key.KDF)
		assert.Empty(t, key.Salt)

		assertOpens(t, open(t, store, encryption.Options{Wrapper: kms}), sealed, "sk-one")

		_, err = encryption.Open(ctx, store, encryption.Options{Key: "local"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `wrapped by key wrapper "kms:test", which is not configured`)

		_, err = encryption.Open(ctx, store, encryption.Options{Wrapper: newFakeKMS(t, "kms:other")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"kms:test"`)
	})
}

func TestOpenSurfacesWrapperErrors(t *testing.T) {
	store := encryptiontest.NewKeyStore()
	kms := newFakeKMS(t, "kms:test")
	kms.fail = errors.New("kms unavailable")
	_, err := encryption.Open(context.Background(), store, encryption.Options{Wrapper: kms})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kms unavailable")
	assert.Empty(t, onlyKeys(t, store), "no key is stored when wrapping fails")
}

func TestRotateDataKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		opts := encryption.Options{Key: "k1"}
		first := open(t, store, opts)
		oldValue := seal(t, first, "sk-one")

		rotated, err := encryption.RotateDataKey(context.Background(), store, opts)
		require.NoError(t, err)
		assert.Equal(t, "2", rotated.ActiveKeyID())
		assertOpens(t, rotated, oldValue, "sk-one")
		assert.False(t, rotated.IsCurrent(oldValue))
		assert.True(t, rotated.IsCurrent(seal(t, rotated, "sk-two")))

		keys := onlyKeys(t, store)
		require.Len(t, keys, 2)
		active := map[string]bool{}
		for _, key := range keys {
			active[key.ID] = key.Active
		}
		assert.Equal(t, map[string]bool{"1": false, "2": true}, active)

		assert.Equal(t, "2", open(t, store, opts).ActiveKeyID())

		_, err = encryption.RotateDataKey(context.Background(), store, encryption.Options{})
		require.Error(t, err)
	})
}

func TestKeyStoreInsertIsFirstWriterWins(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		ctx := context.Background()
		key := encryption.Key{ID: "1", Wrapped: []byte{1, 2, 3}, WrapperID: "w", Active: true, CreatedAt: time.Unix(1700000000, 0).UTC()}
		inserted, err := store.Insert(ctx, key)
		require.NoError(t, err)
		assert.True(t, inserted)

		inserted, err = store.Insert(ctx, encryption.Key{ID: "1", Wrapped: []byte{9}, WrapperID: "other", CreatedAt: time.Now()})
		require.NoError(t, err)
		assert.False(t, inserted)

		got := onlyKey(t, store)
		assert.Equal(t, []byte{1, 2, 3}, got.Wrapped)
		assert.Equal(t, "w", got.WrapperID)
		assert.True(t, got.CreatedAt.Equal(key.CreatedAt))

		require.Error(t, store.Activate(ctx, "missing"))
		require.Error(t, store.UpdateWrapping(ctx, encryption.Key{ID: "missing"}))
		assert.True(t, onlyKey(t, store).Active, "a failed activation leaves the active key alone")
	})
}

func TestOpenNeverLogsKeyMaterial(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	store := encryptiontest.NewKeyStore()
	open(t, store, encryption.Options{Key: "old-secret-key"})
	open(t, store, encryption.Options{Key: "new-secret-key", PreviousKey: "old-secret-key"})
	require.NotEmpty(t, logs.String())
	assert.NotContains(t, logs.String(), "secret-key")
}

func TestRunningBoxPicksUpRotatedDataKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		opts := encryption.Options{Key: "k1"}
		running := open(t, store, opts)

		rotated, err := encryption.RotateDataKey(context.Background(), store, opts)
		require.NoError(t, err)
		value := seal(t, rotated, "sk-new")

		assertOpens(t, running, value, "sk-new")
		assert.Equal(t, "2", running.ActiveKeyID())
	})
}

func TestActivateNeverLeavesNoActiveKey(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		ctx := context.Background()
		for _, id := range []string{"1", "2", "3"} {
			inserted, err := store.Insert(ctx, encryption.Key{ID: id, Wrapped: []byte{1}, WrapperID: "w", Active: id == "1", CreatedAt: time.Now()})
			require.NoError(t, err)
			require.True(t, inserted)
		}
		// Two overlapping rotations finishing out of order.
		require.NoError(t, store.Activate(ctx, "3"))
		require.NoError(t, store.Activate(ctx, "2"))

		active := 0
		for _, key := range onlyKeys(t, store) {
			if key.Active {
				active++
				assert.NotEqual(t, "1", key.ID)
			}
		}
		assert.GreaterOrEqual(t, active, 1)
	})
}

func TestOpenMovesDataKeyBetweenExtensionWrappers(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		ctx := context.Background()
		oldKMS, newKMS := newFakeKMS(t, "kms:old"), newFakeKMS(t, "kms:new")
		sealed := seal(t, open(t, store, encryption.Options{Wrapper: oldKMS}), "sk-one")

		_, err := encryption.Open(ctx, store, encryption.Options{Wrapper: newKMS})
		require.ErrorContains(t, err, "previous key wrapper")

		moved := open(t, store, encryption.Options{Wrapper: newKMS, PreviousWrappers: []encryption.Wrapper{oldKMS}})
		assertOpens(t, moved, sealed, "sk-one")
		assert.Equal(t, "kms:new", onlyKey(t, store).WrapperID)
		assertOpens(t, open(t, store, encryption.Options{Wrapper: newKMS}), sealed, "sk-one")
	})
}

func TestRunningBoxSealsWithRotatedKeyBeforeAnyRead(t *testing.T) {
	runKeyStoreSuite(t, func(t *testing.T, store encryption.KeyStore) {
		opts := encryption.Options{Key: "k1"}
		running := open(t, store, opts)
		_, err := encryption.RotateDataKey(context.Background(), store, opts)
		require.NoError(t, err)

		sealed := seal(t, running, "sk-new")
		assert.Regexp(t, `^enc:v1:2:`, sealed, "a gateway that outlived the rotation seals with the new key")

		a, b := "x", "y"
		require.NoError(t, running.SealFields("kind", "id", encryption.Field{Name: "a", Value: &a}, encryption.Field{Name: "b", Value: &b}))
		assert.Regexp(t, `^enc:v1:2:`, a)
		assert.Regexp(t, `^enc:v1:2:`, b)
	})
}
