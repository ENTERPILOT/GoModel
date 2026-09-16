package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type authKeyTestStore struct {
	keys map[string]authkeys.AuthKey
}

func newAuthKeyTestStore(keys ...authkeys.AuthKey) *authKeyTestStore {
	store := &authKeyTestStore{keys: make(map[string]authkeys.AuthKey, len(keys))}
	for _, key := range keys {
		store.keys[key.ID] = key
	}
	return store
}

func (s *authKeyTestStore) List(_ context.Context) ([]authkeys.AuthKey, error) {
	result := make([]authkeys.AuthKey, 0, len(s.keys))
	for _, key := range s.keys {
		result = append(result, key)
	}
	return result, nil
}

func (s *authKeyTestStore) Create(_ context.Context, key authkeys.AuthKey) error {
	s.keys[key.ID] = key
	return nil
}

func (s *authKeyTestStore) UpdateLabels(_ context.Context, id string, labels []string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.Labels = labels
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
	return nil
}

func (s *authKeyTestStore) UpdateAllowedModels(_ context.Context, id string, allowedModels []string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.AllowedModels = allowedModels
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
	return nil
}

func (s *authKeyTestStore) UpdateDashboardAccess(_ context.Context, id string, allowed bool, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.DashboardAccess = allowed
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
	return nil
}

func (s *authKeyTestStore) Deactivate(_ context.Context, id string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.Enabled = false
	key.UpdatedAt = now.UTC()
	if key.DeactivatedAt == nil {
		deactivatedAt := now.UTC()
		key.DeactivatedAt = &deactivatedAt
	}
	s.keys[id] = key
	return nil
}

func (s *authKeyTestStore) Close() error { return nil }

func newAuthKeyHandler(t *testing.T, store authkeys.Store) *Handler {
	t.Helper()
	service, err := authkeys.NewService(store)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return NewHandler(nil, nil, WithAuthKeys(service))
}

// newAuthKeyHandlerWithReader adds an audit reader and the dashboard runtime
// config carrying the audit retention window, as initAdmin wires them.
func newAuthKeyHandlerWithReader(t *testing.T, store authkeys.Store, reader auditlog.Reader, retentionDays string) *Handler {
	t.Helper()
	service, err := authkeys.NewService(store)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return NewHandler(nil, nil,
		WithAuthKeys(service),
		WithAuditReader(reader),
		WithDashboardRuntimeConfig(DashboardConfigResponse{LoggingRetentionDays: retentionDays}),
	)
}

func TestAuthKeyEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)

	c, rec := echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	c, rec = echotest.Post(t, "/admin/auth-keys", `{"name":"primary"}`)
	require.NoError(t, h.CreateAuthKey(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	c, rec = echotest.Post(t, "/admin/auth-keys/test-key/deactivate", nil, echotest.WithPathValue("id", "test-key"))
	require.NoError(t, h.DeactivateAuthKey(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	c, rec = echotest.Request(t, http.MethodPut, "/admin/auth-keys/test-key/labels", `{"labels":["a"]}`, echotest.WithPathValue("id", "test-key"))
	require.NoError(t, h.UpdateAuthKeyLabels(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	c, rec = echotest.Get(t, "/admin/auth-keys/last-used")
	require.NoError(t, h.GetAuthKeysLastUsed(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func createAuthKey(t *testing.T, h *Handler, body string) authkeys.IssuedKey {
	t.Helper()
	c, rec := echotest.Post(t, "/admin/auth-keys", body)
	require.NoError(t, h.CreateAuthKey(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return echotest.Decode[authkeys.IssuedKey](t, rec)
}

func TestCreateListAndDeactivateAuthKey(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())

	issued := createAuthKey(t, h, `{"name":"primary","description":"prod key","user_path":" team//alpha/service/ ","labels":[" team-a ","batch","team-a"]}`)
	require.NotEmpty(t, issued.Value)
	require.NotEmpty(t, issued.ID)
	assert.Equal(t, "/team/alpha/service", issued.UserPath)
	assert.Equal(t, []string{"team-a", "batch"}, issued.Labels)

	c, rec := echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))
	require.Equal(t, http.StatusOK, rec.Code)
	views := echotest.Decode[[]authkeys.View](t, rec)
	require.Len(t, views, 1)
	assert.True(t, views[0].Active)
	assert.Equal(t, "/team/alpha/service", views[0].UserPath)
	assert.Equal(t, []string{"team-a", "batch"}, views[0].Labels)

	c, rec = echotest.Post(t, "/admin/auth-keys/"+issued.ID+"/deactivate", nil, echotest.WithPathValue("id", issued.ID))
	require.NoError(t, h.DeactivateAuthKey(c))
	require.Equal(t, http.StatusNoContent, rec.Code)

	c, rec = echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))
	views = echotest.Decode[[]authkeys.View](t, rec)
	require.Len(t, views, 1)
	assert.False(t, views[0].Active)
}

func TestUpdateAuthKeyDashboardAccess(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	issued := createAuthKey(t, h, `{"name":"ops","dashboard_access":true}`)
	require.True(t, issued.DashboardAccess)

	updateAccess := func(id, body string) *httptest.ResponseRecorder {
		c, rec := echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+id+"/dashboard-access", body, echotest.WithPathValue("id", id))
		require.NoError(t, h.UpdateAuthKeyDashboardAccess(c))
		return rec
	}

	rec := updateAccess(issued.ID, `{"dashboard_access":false}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, echotest.Decode[authkeys.View](t, rec).DashboardAccess)

	rec = updateAccess("missing", `{"dashboard_access":true}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Omitted or null values must be rejected, not treated as a revoke.
	for _, body := range []string{`{}`, `{"dashboard_access":null}`} {
		rec = updateAccess(issued.ID, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

func TestUpdateAuthKeyLabels(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	issued := createAuthKey(t, h, `{"name":"primary","labels":["old"]}`)

	updateLabels := func(id, body string) *httptest.ResponseRecorder {
		c, rec := echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+id+"/labels", body, echotest.WithPathValue("id", id))
		require.NoError(t, h.UpdateAuthKeyLabels(c))
		return rec
	}

	rec := updateLabels(issued.ID, `{"labels":[" prod ","batch","prod"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"prod", "batch"}, echotest.Decode[authkeys.View](t, rec).Labels)

	rec = updateLabels(issued.ID, `{"labels":[]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, echotest.Decode[authkeys.View](t, rec).Labels)

	rec = updateLabels("missing-id", `{"labels":["x"]}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCreateAuthKeyRejectsInvalidUserPath(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	c, rec := echotest.Post(t, "/admin/auth-keys", `{"name":"primary","user_path":"/team/../alpha"}`)
	require.NoError(t, h.CreateAuthKey(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetAuthKeysLastUsed(t *testing.T) {
	usedAt := time.Date(2026, 1, 16, 12, 30, 0, 0, time.UTC)
	reader := &mockAuditReader{}
	h := newAuthKeyHandlerWithReader(t, newAuthKeyTestStore(), reader, "30")
	usedKey := createAuthKey(t, h, `{"name":"used"}`)
	unusedKey := createAuthKey(t, h, `{"name":"unused"}`)
	reader.lastUsed = map[string]time.Time{usedKey.ID: usedAt}

	c, rec := echotest.Get(t, "/admin/auth-keys/last-used")
	require.NoError(t, h.GetAuthKeysLastUsed(c))
	require.Equal(t, http.StatusOK, rec.Code)
	body := echotest.Decode[map[string]any](t, rec)

	lastUsed, ok := body["last_used"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, usedAt.Format(time.RFC3339), lastUsed[usedKey.ID])
	_, ok = lastUsed[unusedKey.ID]
	assert.False(t, ok)
	assert.Equal(t, float64(30), body["retention_days"])

	// Every listed key id reached the reader in one call.
	assert.ElementsMatch(t, []string{usedKey.ID, unusedKey.ID}, reader.lastUsedKeyIDs)
}

func TestGetAuthKeysLastUsedShortCircuitsWithoutKeys(t *testing.T) {
	reader := &mockAuditReader{}
	h := newAuthKeyHandlerWithReader(t, newAuthKeyTestStore(), reader, "30")

	c, rec := echotest.Get(t, "/admin/auth-keys/last-used")
	require.NoError(t, h.GetAuthKeysLastUsed(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, reader.lastUsedKeyIDs)

	body := echotest.Decode[map[string]any](t, rec)
	lastUsed, ok := body["last_used"].(map[string]any)
	require.True(t, ok)
	assert.Empty(t, lastUsed)
	assert.Equal(t, float64(30), body["retention_days"])

	// An unwired retention config surfaces 0 (unknown window), not an error.
	h = newAuthKeyHandlerWithReader(t, newAuthKeyTestStore(), reader, "")
	c, rec = echotest.Get(t, "/admin/auth-keys/last-used")
	require.NoError(t, h.GetAuthKeysLastUsed(c))
	assert.Equal(t, float64(0), echotest.Decode[map[string]any](t, rec)["retention_days"])
}

func TestGetAuthKeysLastUsedReturns503OnReaderError(t *testing.T) {
	reader := &mockAuditReader{lastUsedErr: errors.New("audit reader down")}
	h := newAuthKeyHandlerWithReader(t, newAuthKeyTestStore(), reader, "30")
	createAuthKey(t, h, `{"name":"any"}`)

	c, rec := echotest.Get(t, "/admin/auth-keys/last-used")
	require.NoError(t, h.GetAuthKeysLastUsed(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
