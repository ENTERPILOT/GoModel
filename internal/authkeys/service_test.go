package authkeys

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func (s *testStore) UpdateLabels(_ context.Context, id string, labels []string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return ErrNotFound
	}
	key.Labels = labels
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
	return nil
}

func (s *testStore) UpdateAllowedModels(_ context.Context, id string, allowedModels []string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return ErrNotFound
	}
	key.AllowedModels = allowedModels
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
	return nil
}

func (s *testStore) UpdateDashboardAccess(_ context.Context, id string, allowed bool, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return ErrNotFound
	}
	key.DashboardAccess = allowed
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
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
	require.NoError(t, err)
	require.False(t, service.Enabled())

	issued, err := service.Create(context.Background(), CreateInput{Name: "primary"})
	require.NoError(t, err)
	require.NotNil(t, issued)
	require.Greater(t, len(issued.Value), len(TokenPrefix))
	require.Equal(t, TokenPrefix, issued.Value[:len(TokenPrefix)])
	require.True(t, service.Enabled())

	authKeyID, err := service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.Equal(t, issued.ID, authKeyID.ID)
	err = service.Deactivate(context.Background(), issued.ID)
	require.NoError(t, err)
	_, err = service.Authenticate(context.Background(), issued.Value)
	require.ErrorIs(t, err, ErrInactive)

	views := service.ListViews()
	require.Len(t, views, 1)
	require.False(t, views[0].Active)
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
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)
	_, err = service.Authenticate(context.Background(), TokenPrefix+"secret")
	require.ErrorIs(t, err, ErrExpired)
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
	require.NoError(t, err)

	service.snapshot = snapshot{
		order:        []string{key.ID},
		byID:         map[string]AuthKey{key.ID: key},
		bySecretHash: map[string]AuthKey{key.SecretHash: key},
		activeByHash: map[string]AuthKey{key.SecretHash: key},
	}
	_, err = service.Authenticate(context.Background(), TokenPrefix+"secret")
	require.ErrorIs(t, err, ErrExpired)
}

func TestServiceWriteOperationsIgnoreRefreshReconciliationFailures(t *testing.T) {
	t.Run("create still succeeds when refresh reconciliation fails", func(t *testing.T) {
		store := newTestStore()
		service, err := NewService(store)
		require.NoError(t, err)

		store.listErr = errors.New("transient list failure")
		issued, err := service.Create(context.Background(), CreateInput{Name: "primary"})
		require.NoError(t, err)
		require.NotNil(t, issued)
		got, err := service.Authenticate(context.Background(), issued.Value)
		require.NoError(t, err)
		require.Equal(t, issued.ID, got.ID)
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
		require.NoError(t, err)
		err = service.Refresh(context.Background())
		require.NoError(t, err)

		store.listErr = errors.New("transient list failure")
		err = service.Deactivate(context.Background(), key.ID)
		require.NoError(t, err)
		_, err = service.Authenticate(context.Background(), TokenPrefix+"secret")
		require.ErrorIs(t, err, ErrInactive)
	})
}

func TestServiceCreateNormalizesUserPathAndReturnsItOnAuthenticate(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	issued, err := service.Create(context.Background(), CreateInput{
		Name:     "scoped",
		UserPath: " team//alpha/service/ ",
	})
	require.NoError(t, err)
	require.Equal(t, "/team/alpha/service", issued.UserPath)

	authenticated, err := service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.Equal(t, "/team/alpha/service", authenticated.UserPath)
}

func TestServiceCreateNormalizesLabelsAndReturnsThemOnAuthenticate(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	issued, err := service.Create(context.Background(), CreateInput{
		Name:   "labelled",
		Labels: []string{" team-a ", "batch", "team-a", ""},
	})
	require.NoError(t, err)

	want := []string{"team-a", "batch"}
	require.Equal(t, want, issued.Labels)

	authenticated, err := service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.Equal(t, want, authenticated.Labels)
}

func TestServiceUpdateLabelsAppliesImmediatelyToAuthenticate(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	issued, err := service.Create(context.Background(), CreateInput{
		Name:   "labelled",
		Labels: []string{"old"},
	})
	require.NoError(t, err)

	view, err := service.UpdateLabels(context.Background(), issued.ID, []string{" new-a ", "new-b", "new-a", ""})
	require.NoError(t, err)

	want := []string{"new-a", "new-b"}
	require.Equal(t, want, view.Labels)

	authenticated, err := service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.Equal(t, want, authenticated.Labels)

	cleared, err := service.UpdateLabels(context.Background(), issued.ID, nil)
	require.NoError(t, err)
	require.Nil(t, cleared.Labels)

	authenticated, err = service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.Nil(t, authenticated.Labels)
}

func TestServiceDashboardAccessLifecycle(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	issued, err := service.Create(context.Background(), CreateInput{
		Name:            "ops",
		DashboardAccess: true,
	})
	require.NoError(t, err)
	require.True(t, issued.DashboardAccess)

	authenticated, err := service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.True(t, authenticated.DashboardAccess)

	view, err := service.UpdateDashboardAccess(context.Background(), issued.ID, false)
	require.NoError(t, err)
	require.False(t, view.DashboardAccess)

	authenticated, err = service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.False(t, authenticated.DashboardAccess)
	_, err = service.UpdateDashboardAccess(context.Background(), "missing", true)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestServiceCreateDefaultsToNoDashboardAccess(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	issued, err := service.Create(context.Background(), CreateInput{Name: "plain"})
	require.NoError(t, err)
	require.False(t, issued.DashboardAccess)

	authenticated, err := service.Authenticate(context.Background(), issued.Value)
	require.NoError(t, err)
	require.False(t, authenticated.DashboardAccess)
}

func TestServiceUpdateLabelsUnknownKeyReturnsNotFound(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)
	_, err = service.UpdateLabels(context.Background(), "missing", []string{"x"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestServiceCreateRejectsInvalidUserPath(t *testing.T) {
	service, err := NewService(newTestStore())
	require.NoError(t, err)

	_, err = service.Create(context.Background(), CreateInput{
		Name:     "invalid",
		UserPath: "/team/../alpha",
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}
