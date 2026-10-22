package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/core"
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

func (s *authKeyTestStore) UpdateImported(_ context.Context, key authkeys.AuthKey) error {
	if _, ok := s.keys[key.ID]; !ok {
		return authkeys.ErrNotFound
	}
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

func (s *authKeyTestStore) UpdatePassthroughAccess(_ context.Context, id string, allowed bool, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.PassthroughAccess = allowed
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

// newAuthKeyHandlerWithReader adds an audit reader, as initAdmin wires it.
func newAuthKeyHandlerWithReader(t *testing.T, store authkeys.Store, reader auditlog.Reader) *Handler {
	t.Helper()
	service, err := authkeys.NewService(store)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return NewHandler(nil, nil,
		WithAuthKeys(service),
		WithAuditReader(reader),
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

func TestUpdateAuthKeyPassthroughAccess(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	assert.False(t, createAuthKey(t, h, `{"name":"plain"}`).PassthroughAccess, "new keys start without passthrough")
	issued := createAuthKey(t, h, `{"name":"proxy","passthrough_access":true}`)
	require.True(t, issued.PassthroughAccess)

	updateAccess := func(id, body string) *httptest.ResponseRecorder {
		c, rec := echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+id+"/passthrough-access", body, echotest.WithPathValue("id", id))
		require.NoError(t, h.UpdateAuthKeyPassthroughAccess(c))
		return rec
	}

	rec := updateAccess(issued.ID, `{"passthrough_access":false}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, echotest.Decode[authkeys.View](t, rec).PassthroughAccess)

	rec = updateAccess("missing", `{"passthrough_access":true}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Omitted or null values must be rejected, not treated as a revoke.
	for _, body := range []string{`{}`, `{"passthrough_access":null}`} {
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

func TestListAuthKeysIncludesLastUsed(t *testing.T) {
	usedAt := time.Date(2026, 1, 16, 12, 30, 0, 0, time.UTC)
	reader := &mockAuditReader{}
	h := newAuthKeyHandlerWithReader(t, newAuthKeyTestStore(), reader)
	usedKey := createAuthKey(t, h, `{"name":"used"}`)
	unusedKey := createAuthKey(t, h, `{"name":"unused"}`)
	reader.lastUsed = map[string]time.Time{usedKey.ID: usedAt}

	c, rec := echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))
	require.Equal(t, http.StatusOK, rec.Code)
	body := echotest.Decode[[]map[string]any](t, rec)

	rowsByID := make(map[string]map[string]any, len(body))
	for _, row := range body {
		rowsByID[row["id"].(string)] = row
	}
	assert.Equal(t, usedAt.Format(time.RFC3339), rowsByID[usedKey.ID]["last_used_at"])
	_, ok := rowsByID[unusedKey.ID]["last_used_at"]
	assert.False(t, ok)
	// A successful lookup (even without entries) reports availability.
	assert.Equal(t, true, rowsByID[usedKey.ID]["last_used_available"])
	assert.Equal(t, true, rowsByID[unusedKey.ID]["last_used_available"])

	// Every listed key id reached the reader in one call.
	assert.ElementsMatch(t, []string{usedKey.ID, unusedKey.ID}, reader.lastUsedKeyIDs)
}

func TestListAuthKeysWorksWithoutLastUsedData(t *testing.T) {
	// Without an audit reader the list still answers, with no last-used data.
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	createAuthKey(t, h, `{"name":"any"}`)
	c, rec := echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := echotest.Decode[[]map[string]any](t, rec)
	require.Len(t, body, 1)
	_, ok := body[0]["last_used_at"]
	assert.False(t, ok)
	_, ok = body[0]["last_used_available"]
	assert.False(t, ok)

	// A failing reader degrades the same way.
	reader := &mockAuditReader{lastUsedErr: errors.New("audit reader down")}
	h = newAuthKeyHandlerWithReader(t, newAuthKeyTestStore(), reader)
	createAuthKey(t, h, `{"name":"any"}`)
	c, rec = echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	body = echotest.Decode[[]map[string]any](t, rec)
	require.Len(t, body, 1)
	_, ok = body[0]["last_used_at"]
	assert.False(t, ok)
	_, ok = body[0]["last_used_available"]
	assert.False(t, ok)
}

// asManagedKey makes c look like a request authenticated with the managed key
// id, bound to userPath and limited to allowedModels.
func asManagedKey(c *echo.Context, id, userPath string, allowedModels ...string) {
	ctx := core.WithAuthKeyID(c.Request().Context(), id)
	ctx = core.WithAccessScope(ctx, core.AccessScope{UserPath: userPath})
	ctx = core.WithCredentialAllowedModels(ctx, allowedModels)
	c.SetRequest(c.Request().WithContext(ctx))
}

// A key must not lift restrictions placed on it by editing itself.
func TestAuthKeyCannotUpdateItself(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	lead := createAuthKey(t, h, `{"name":"lead","user_path":"/team/a","dashboard_access":true,"allowed_models":["openai/gpt-4o-mini"],"labels":["team-a"]}`)
	member := createAuthKey(t, h, `{"name":"member","user_path":"/team/a/svc","allowed_models":["openai/gpt-4o-mini"]}`)

	tests := []struct {
		name    string
		path    string
		body    string
		handler func(*echo.Context) error
	}{
		{name: "labels", path: "labels", body: `{"labels":[]}`, handler: h.UpdateAuthKeyLabels},
		{name: "allowed models", path: "allowed-models", body: `{"allowed_models":["openai/gpt-4o-mini"]}`, handler: h.UpdateAuthKeyAllowedModels},
		{name: "dashboard access", path: "dashboard-access", body: `{"dashboard_access":true}`, handler: h.UpdateAuthKeyDashboardAccess},
		{name: "passthrough access", path: "passthrough-access", body: `{"passthrough_access":false}`, handler: h.UpdateAuthKeyPassthroughAccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			update := func(id string) *httptest.ResponseRecorder {
				c, rec := echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+id+"/"+tt.path, tt.body, echotest.WithPathValue("id", id))
				asManagedKey(c, lead.ID, "/team/a", "openai/gpt-4o-mini")
				require.NoError(t, tt.handler(c))
				return rec
			}

			rec := update(lead.ID)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), codeAuthKeySelfUpdate)

			rec = update(member.ID)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}

	view, err := h.authKeys.View(lead.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"openai/gpt-4o-mini"}, view.AllowedModels)
	assert.Equal(t, []string{"team-a"}, view.Labels)

	// Deactivating itself stays allowed.
	c, rec := echotest.Post(t, "/admin/auth-keys/"+lead.ID+"/deactivate", nil, echotest.WithPathValue("id", lead.ID))
	asManagedKey(c, lead.ID, "/team/a", "openai/gpt-4o-mini")
	require.NoError(t, h.DeactivateAuthKey(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// A key limited to a model allowlist must not issue keys broader than itself.
func TestAuthKeyAllowedModelsMustStayWithinCaller(t *testing.T) {
	tests := []struct {
		name         string
		callerModels []string
		body         string
		wantStatus   int
	}{
		{name: "subset", callerModels: []string{"openai/gpt-4o-mini", "openai/gpt-4o"}, body: `["openai/gpt-4o-mini"]`, wantStatus: http.StatusOK},
		{name: "same list", callerModels: []string{"openai/gpt-4o-mini"}, body: `["openai/gpt-4o-mini"]`, wantStatus: http.StatusOK},
		{name: "empty list is unrestricted", callerModels: []string{"openai/gpt-4o-mini"}, body: `[]`, wantStatus: http.StatusForbidden},
		{name: "model outside the caller's list", callerModels: []string{"openai/gpt-4o-mini"}, body: `["openai/gpt-4o"]`, wantStatus: http.StatusForbidden},
		{name: "unrestricted caller", body: `[]`, wantStatus: http.StatusOK},
		{name: "provider-wide caller grants one model", callerModels: []string{"openai/"}, body: `["openai/gpt-4o-mini"]`, wantStatus: http.StatusOK},
		{name: "provider-wide caller grants another provider", callerModels: []string{"openai/"}, body: `["anthropic/claude-sonnet-4-6"]`, wantStatus: http.StatusForbidden},
		{name: "global selector caller grants unrestricted", callerModels: []string{"/"}, body: `[]`, wantStatus: http.StatusOK},
		{name: "bare name caller grants that model on one provider", callerModels: []string{"gpt-4o-mini"}, body: `["openai/gpt-4o-mini"]`, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/create", func(t *testing.T) {
			h := newAuthKeyHandler(t, newAuthKeyTestStore())
			c, rec := echotest.Post(t, "/admin/auth-keys", `{"name":"child","allowed_models":`+tt.body+`}`)
			asManagedKey(c, "caller", "/team/a", tt.callerModels...)
			require.NoError(t, h.CreateAuthKey(c))
			wantStatus := tt.wantStatus
			if wantStatus == http.StatusOK {
				wantStatus = http.StatusCreated
			}
			assert.Equal(t, wantStatus, rec.Code, rec.Body.String())
		})
		t.Run(tt.name+"/update", func(t *testing.T) {
			h := newAuthKeyHandler(t, newAuthKeyTestStore())
			child := createAuthKey(t, h, `{"name":"child","user_path":"/team/a/svc","allowed_models":["openai/gpt-4o-mini"]}`)
			c, rec := echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+child.ID+"/allowed-models", `{"allowed_models":`+tt.body+`}`, echotest.WithPathValue("id", child.ID))
			asManagedKey(c, "caller", "/team/a", tt.callerModels...)
			require.NoError(t, h.UpdateAuthKeyAllowedModels(c))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
}
