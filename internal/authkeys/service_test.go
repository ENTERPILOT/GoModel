package authkeys

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testStore struct {
	keys          map[string]AuthKey
	listErr       error
	createErr     error
	deactivateErr error
}

func newTestStore(keys ...AuthKey) *testStore {
	store := &testStore{keys: make(map[string]AuthKey, len(keys))}
	for _, key := range keys {
		store.keys[key.ID] = key
	}
	return store
}

func (s *testStore) List(_ context.Context) ([]AuthKey, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	result := make([]AuthKey, 0, len(s.keys))
	for _, key := range s.keys {
		result = append(result, key)
	}
	return result, nil
}

func (s *testStore) Create(_ context.Context, key AuthKey) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.keys[key.ID] = key
	return nil
}

func (s *testStore) Deactivate(_ context.Context, id string, now time.Time) error {
	if s.deactivateErr != nil {
		return s.deactivateErr
	}
	key, ok := s.keys[id]
	if !ok {
		return ErrNotFound
	}
	key.Enabled = false
	key.UpdatedAt = now.UTC()
	if key.DeactivatedAt == nil {
		timestamp := now.UTC()
		key.DeactivatedAt = &timestamp
	}
	s.keys[id] = key
	return nil
}

func (s *testStore) Close() error { return nil }

func TestServiceCreateAuthenticateAndDeactivate(t *testing.T) {
	service, err := NewService(newTestStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if service.Enabled() {
		t.Fatal("Enabled() = true, want false before any keys exist")
	}

	issued, err := service.Create(context.Background(), CreateInput{Name: "primary"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued == nil {
		t.Fatal("Create() = nil, want issued key")
	}
	if len(issued.Value) <= len(TokenPrefix) || issued.Value[:len(TokenPrefix)] != TokenPrefix {
		t.Fatalf("issued value = %q, want %q prefix", issued.Value, TokenPrefix)
	}
	if !service.Enabled() {
		t.Fatal("Enabled() = false, want true after create")
	}

	authKeyID, err := service.Authenticate(context.Background(), issued.Value)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if authKeyID != issued.ID {
		t.Fatalf("Authenticate() id = %q, want %q", authKeyID, issued.ID)
	}

	if err := service.Deactivate(context.Background(), issued.ID); err != nil {
		t.Fatalf("Deactivate() error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), issued.Value); err != ErrInactive {
		t.Fatalf("Authenticate() after deactivate error = %v, want %v", err, ErrInactive)
	}

	views := service.ListViews()
	if len(views) != 1 {
		t.Fatalf("ListViews() len = %d, want 1", len(views))
	}
	if views[0].Active {
		t.Fatal("ListViews()[0].Active = true, want false after deactivation")
	}
}

func TestServiceAuthenticateExpiredKey(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-time.Minute)
	key := AuthKey{
		ID:            "key-expired",
		Name:          "expired",
		RedactedValue: TokenPrefix + "...zzzz",
		SecretHash:    hashSecret("secret"),
		Enabled:       true,
		ExpiresAt:     &expiredAt,
		CreatedAt:     time.Now().UTC().Add(-2 * time.Hour),
		UpdatedAt:     time.Now().UTC().Add(-2 * time.Hour),
	}
	service, err := NewService(newTestStore(key))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if _, err := service.Authenticate(context.Background(), TokenPrefix+"secret"); err != ErrExpired {
		t.Fatalf("Authenticate() error = %v, want %v", err, ErrExpired)
	}
}

func TestServiceAuthenticateRechecksStaleActiveSnapshot(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-time.Minute)
	key := AuthKey{
		ID:            "key-expired",
		Name:          "expired",
		RedactedValue: TokenPrefix + "...zzzz",
		SecretHash:    hashSecret("secret"),
		Enabled:       true,
		ExpiresAt:     &expiredAt,
		CreatedAt:     time.Now().UTC().Add(-2 * time.Hour),
		UpdatedAt:     time.Now().UTC().Add(-2 * time.Hour),
	}
	service, err := NewService(newTestStore())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.snapshot = snapshot{
		order:        []string{key.ID},
		byID:         map[string]AuthKey{key.ID: key},
		bySecretHash: map[string]AuthKey{key.SecretHash: key},
		activeByHash: map[string]AuthKey{key.SecretHash: key},
	}

	if _, err := service.Authenticate(context.Background(), TokenPrefix+"secret"); err != ErrExpired {
		t.Fatalf("Authenticate() error = %v, want %v", err, ErrExpired)
	}
}

func TestServiceWriteOperationsIgnoreRefreshReconciliationFailures(t *testing.T) {
	t.Run("create still succeeds when refresh reconciliation fails", func(t *testing.T) {
		store := newTestStore()
		service, err := NewService(store)
		if err != nil {
			t.Fatalf("NewService() error = %v", err)
		}

		store.listErr = errors.New("transient list failure")
		issued, err := service.Create(context.Background(), CreateInput{Name: "primary"})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if issued == nil {
			t.Fatal("Create() = nil, want issued key")
		}
		if got, err := service.Authenticate(context.Background(), issued.Value); err != nil || got != issued.ID {
			t.Fatalf("Authenticate() = (%q, %v), want (%q, nil)", got, err, issued.ID)
		}
	})

	t.Run("deactivate still succeeds when refresh reconciliation fails", func(t *testing.T) {
		key := AuthKey{
			ID:            "key-1",
			Name:          "primary",
			RedactedValue: TokenPrefix + "...abcd",
			SecretHash:    hashSecret("secret"),
			Enabled:       true,
			CreatedAt:     time.Now().UTC().Add(-time.Hour),
			UpdatedAt:     time.Now().UTC().Add(-time.Hour),
		}
		store := newTestStore(key)
		service, err := NewService(store)
		if err != nil {
			t.Fatalf("NewService() error = %v", err)
		}
		if err := service.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}

		store.listErr = errors.New("transient list failure")
		if err := service.Deactivate(context.Background(), key.ID); err != nil {
			t.Fatalf("Deactivate() error = %v", err)
		}
		if _, err := service.Authenticate(context.Background(), TokenPrefix+"secret"); err != ErrInactive {
			t.Fatalf("Authenticate() error = %v, want %v", err, ErrInactive)
		}
	})
}
