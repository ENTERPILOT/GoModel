// Package encryptiontest provides an in-memory key store and ready Boxes for
// tests of stores that seal secrets.
package encryptiontest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption"
)

// KeyStore is an in-memory encryption.KeyStore.
type KeyStore struct {
	mu   sync.Mutex
	keys []encryption.Key
}

// NewKeyStore returns an empty in-memory key store.
func NewKeyStore() *KeyStore {
	return &KeyStore{}
}

func (s *KeyStore) List(context.Context) ([]encryption.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.keys), nil
}

func (s *KeyStore) Insert(_ context.Context, key encryption.Key) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.keys {
		if existing.ID == key.ID {
			return false, nil
		}
	}
	s.keys = append(s.keys, key)
	return true, nil
}

func (s *KeyStore) UpdateWrapping(_ context.Context, key encryption.Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].ID == key.ID {
			s.keys[i].Wrapped, s.keys[i].WrapperID = key.Wrapped, key.WrapperID
			s.keys[i].KDF, s.keys[i].KDFParams, s.keys[i].Salt = key.KDF, key.KDFParams, key.Salt
			return nil
		}
	}
	return fmt.Errorf("key %q not found", key.ID)
}

func (s *KeyStore) Activate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.ContainsFunc(s.keys, func(k encryption.Key) bool { return k.ID == id }) {
		return fmt.Errorf("key %q not found", id)
	}
	for i := range s.keys {
		s.keys[i].Active = s.keys[i].ID == id
	}
	return nil
}

// Key is the GOMODEL_ENCRYPTION_KEY the helpers use.
const Key = "test-encryption-key"

// NewBox returns an enabled Box over a fresh in-memory key store, together
// with the store so a test can rotate the data key.
func NewBox(t testing.TB) (*encryption.Box, *KeyStore) {
	t.Helper()
	store := NewKeyStore()
	box, err := encryption.Open(context.Background(), store, encryption.Options{Key: Key})
	require.NoError(t, err)
	return box, store
}

// Rotate creates and activates a new data key in store and returns the Box
// that seals with it while still opening values of the older keys.
func Rotate(t testing.TB, store *KeyStore) *encryption.Box {
	t.Helper()
	box, err := encryption.RotateDataKey(context.Background(), store, encryption.Options{Key: Key})
	require.NoError(t, err)
	return box
}
