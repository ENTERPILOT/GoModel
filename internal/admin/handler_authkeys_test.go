package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
