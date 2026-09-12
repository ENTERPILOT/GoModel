package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/users"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

type userTestCatalog []string

func (c userTestCatalog) ProviderNames() []string { return []string(c) }

func newUsersHandler(t *testing.T, keys ...authkeys.AuthKey) *Handler {
	t.Helper()
	ctx := context.Background()
	store, err := users.NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)

	userService, err := users.NewService(store, userTestCatalog{"openai", "anthropic"})
	require.NoError(t, err)
	err = userService.Refresh(ctx)
	require.NoError(t, err)

	keyService, err := authkeys.NewService(newAuthKeyTestStore(keys...))
	require.NoError(t, err)
	err = keyService.Refresh(ctx)
	require.NoError(t, err)

	return NewHandler(nil, nil, WithUsers(userService), WithAuthKeys(keyService))
}

func decodeUsers(t *testing.T, rec *httptest.ResponseRecorder) map[string]userNodeResponse {
	t.Helper()
	resp := echotest.Decode[userListResponse](t, rec)

	byPath := make(map[string]userNodeResponse, len(resp.Users))
	for i, node := range resp.Users {
		if i > 0 {
			require.LessOrEqual(t, resp.Users[i-1].UserPath, node.UserPath, "users not sorted by path: %v", resp.Users)
		}
		byPath[node.UserPath] = node
	}
	return byPath
}

func TestUsersEndpointsReturn503WhenUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	for _, tc := range []struct {
		name string
		run  func(*echo.Context) error
	}{
		{name: "list", run: h.ListUsers},
		{name: "upsert", run: h.UpsertUser},
		{name: "delete", run: h.DeleteUser},
	} {
		c, rec := echotest.Get(t, "/admin/users")
		require.NoError(t, tc.run(c))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, tc.name)
	}
}

func TestUsersTreeDerivesFromPoliciesAndKeys(t *testing.T) {
	now := time.Now().UTC()
	h := newUsersHandler(t,
		authkeys.AuthKey{ID: "k1", Name: "alice", UserPath: "/acme/eng/alice", SecretHash: "h1", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "k2", Name: "alice-2", UserPath: "/acme/eng/alice", SecretHash: "h2", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "k3", Name: "ops", UserPath: "/acme/ops", SecretHash: "h3", Enabled: true, CreatedAt: now, UpdatedAt: now},
	)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"acme/eng","allowed_models":["anthropic/*"," "],"description":"eng"}`)
	require.NoError(t, h.UpsertUser(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	c, rec = echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/acme","allowed_models":["openai/*","anthropic/*"]}`)
	require.NoError(t, h.UpsertUser(c))

	nodes := decodeUsers(t, rec)

	for _, path := range []string{"/", "/acme", "/acme/eng", "/acme/eng/alice", "/acme/ops"} {
		_, ok := nodes[path]
		require.True(t, ok, path)
	}
	require.Len(t, nodes, 5)

	eng := nodes["/acme/eng"]
	require.True(t, eng.Configured)
	require.Equal(t, "eng", eng.Description)
	require.Equal(t, []string{"anthropic/"}, eng.AllowedModels)
	require.Equal(t, 0, eng.KeyCount)
	require.Equal(t, []string{"/acme"}, eng.InheritedFrom)

	alice := nodes["/acme/eng/alice"]
	require.False(t, alice.Configured)
	require.Equal(t, 2, alice.KeyCount)
	require.Empty(t, alice.AllowedModels)
	require.Equal(t, []string{"/acme", "/acme/eng"}, alice.InheritedFrom)
	ops := nodes["/acme/ops"]
	require.Equal(t, 1, ops.KeyCount)
	require.Equal(t, []string{"/acme"}, ops.InheritedFrom)
	root := nodes["/"]
	require.False(t, root.Configured)
	require.Empty(t, root.InheritedFrom)

	c, rec = echotest.Request(t, http.MethodDelete, "/admin/users?user_path=/acme/eng", nil)
	require.NoError(t, h.DeleteUser(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	nodes = decodeUsers(t, rec)
	// The node stays as an implied ancestor of alice's keys.
	require.Contains(t, nodes, "/acme/eng")
	require.False(t, nodes["/acme/eng"].Configured, "/acme/eng after delete")

	c, rec = echotest.Request(t, http.MethodDelete, "/admin/users?user_path=/acme/eng", nil)
	require.NoError(t, h.DeleteUser(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUsersTreeCountsActiveKeys(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	deactivated := now.Add(-2 * time.Hour)
	h := newUsersHandler(t,
		authkeys.AuthKey{ID: "live", Name: "live", UserPath: "/acme/live", SecretHash: "h1", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "dead", Name: "dead", UserPath: "/acme/dead", SecretHash: "h2", Enabled: true, DeactivatedAt: &deactivated, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "stale", Name: "stale", UserPath: "/acme/stale", SecretHash: "h3", Enabled: true, ExpiresAt: &expired, CreatedAt: now, UpdatedAt: now},
	)

	c, rec := echotest.Get(t, "/admin/users")
	require.NoError(t, h.ListUsers(c))

	nodes := decodeUsers(t, rec)

	for path, want := range map[string][2]int{
		"/acme/live":  {1, 1},
		"/acme/dead":  {1, 0},
		"/acme/stale": {1, 0},
	} {
		node, ok := nodes[path]
		require.True(t, ok, path)
		require.Equal(t, want[0], node.KeyCount, path)
		require.Equal(t, want[1], node.ActiveKeyCount, path)
	}
	require.Equal(t, 0, nodes["/"].KeyCount)
	require.Equal(t, 0, nodes["/"].ActiveKeyCount)
}

func TestUsersTreeReportsEffectiveModels(t *testing.T) {
	ctx := context.Background()
	registry := newVMModelRegistry(t) // openai/gpt-4o only
	store, err := users.NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)

	userService, err := users.NewService(store, registry)
	require.NoError(t, err)
	err = userService.Refresh(ctx)
	require.NoError(t, err)

	vmService := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/acme"}, Enabled: true,
	})
	vmService.SetAccessPolicy(userService)
	h := NewHandler(nil, registry, WithUsers(userService), WithVirtualModels(vmService))

	c, _ := echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/acme/eng","allowed_models":["gpt-4o"]}`)
	require.NoError(t, h.UpsertUser(c))

	c, _ = echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/acme/eng/bob","allowed_models":["openai/gpt-5"]}`)
	require.NoError(t, h.UpsertUser(c))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/other","allowed_models":["openai/*"]}`)
	require.NoError(t, h.UpsertUser(c))

	nodes := decodeUsers(t, rec)
	// /acme: unrestricted on the user side, model-side row allows gpt-4o here.
	n := nodes["/acme"]
	require.False(t, n.Restricted)
	require.Equal(t, []string{"openai/gpt-4o"}, n.EffectiveModels)
	// /acme/eng: own model-wide selector keeps gpt-4o.
	n = nodes["/acme/eng"]
	require.True(t, n.Restricted)
	require.Equal(t, []string{"openai/gpt-4o"}, n.EffectiveModels)
	// /acme/eng/bob: intersection with eng is empty.
	n = nodes["/acme/eng/bob"]
	require.True(t, n.Restricted)
	require.Empty(t, n.EffectiveModels)
	require.NotNil(t, n.EffectiveModels)
	// /other: user side allows openai, but the model-side row scopes gpt-4o to /acme.
	n = nodes["/other"]
	require.True(t, n.Restricted)
	require.Empty(t, n.EffectiveModels)
}

func TestListAuthKeysReportsEffectiveModels(t *testing.T) {
	ctx := context.Background()
	registry := newVMModelRegistry(t) // openai/gpt-4o only
	store, err := users.NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	require.NoError(t, err)

	userService, err := users.NewService(store, registry)
	require.NoError(t, err)
	err = userService.Refresh(ctx)
	require.NoError(t, err)

	vmService := newVMServiceForRegistry(t, registry, true)
	vmService.SetAccessPolicy(userService)
	now := time.Now().UTC()
	keyService, err := authkeys.NewService(newAuthKeyTestStore(
		authkeys.AuthKey{ID: "open", Name: "open", UserPath: "/acme", SecretHash: "h1", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "narrow", Name: "narrow", UserPath: "/acme", AllowedModels: []string{"anthropic/"}, SecretHash: "h2", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "path", Name: "path", UserPath: "/sales", SecretHash: "h3", Enabled: true, CreatedAt: now, UpdatedAt: now},
	))
	require.NoError(t, err)
	err = keyService.Refresh(ctx)
	require.NoError(t, err)
	_, err = userService.Upsert(ctx, users.User{UserPath: "/sales", AllowedModels: []string{"gpt-4o"}})
	require.NoError(t, err)

	h := NewHandler(nil, registry, WithUsers(userService), WithAuthKeys(keyService), WithVirtualModels(vmService))

	c, rec := echotest.Get(t, "/admin/auth-keys")
	require.NoError(t, h.ListAuthKeys(c))

	byID := map[string]authKeyResponse{}
	for _, row := range echotest.Decode[[]authKeyResponse](t, rec) {
		byID[row.ID] = row
	}
	r := byID["open"]
	require.False(t, r.Restricted)
	require.Equal(t, []string{"openai/gpt-4o"}, r.EffectiveModels)
	r = byID["narrow"]
	require.True(t, r.Restricted)
	require.Empty(t, r.EffectiveModels)
	require.NotNil(t, r.EffectiveModels)
	// Restricted by the path policy only; its selector still admits gpt-4o.
	r = byID["path"]
	require.True(t, r.Restricted)
	require.Equal(t, []string{"openai/gpt-4o"}, r.EffectiveModels)
}

func TestUpsertUserRejectsUnknownProvider(t *testing.T) {
	h := newUsersHandler(t)
	c, rec := echotest.Request(t, http.MethodPut, "/admin/users", `{"user_path":"/acme","allowed_models":["nope/*"]}`)
	require.NoError(t, h.UpsertUser(c))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestAuthKeyAllowedModelsCreateAndUpdate(t *testing.T) {
	h := newUsersHandler(t)

	c, rec := echotest.Post(t, "/admin/auth-keys", `{"name":"restricted","user_path":"/acme/eng","allowed_models":["anthropic/*","openai/gpt-4o","anthropic/*"]}`)
	require.NoError(t, h.CreateAuthKey(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	issued := echotest.Decode[authkeys.IssuedKey](t, rec)
	require.Equal(t, []string{"anthropic/", "openai/gpt-4o"}, issued.AllowedModels)

	c, rec = echotest.Post(t, "/admin/auth-keys", `{"name":"bad","allowed_models":["nope/*"]}`)
	require.NoError(t, h.CreateAuthKey(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	c, rec = echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+issued.ID+"/allowed-models", `{"allowed_models":["openai/*"]}`, echotest.WithPathValue("id", issued.ID))
	require.NoError(t, h.UpdateAuthKeyAllowedModels(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[authkeys.View](t, rec)
	require.Equal(t, []string{"openai/"}, view.AllowedModels)

	c, rec = echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+issued.ID+"/allowed-models", `{"allowed_models":[]}`, echotest.WithPathValue("id", issued.ID))
	require.NoError(t, h.UpdateAuthKeyAllowedModels(c))

	cleared := echotest.Decode[authkeys.View](t, rec)
	require.Empty(t, cleared.AllowedModels)

	for _, body := range []string{`{}`, `{"allowed_models":null}`} {
		c, rec = echotest.Request(t, http.MethodPut, "/admin/auth-keys/"+issued.ID+"/allowed-models", body, echotest.WithPathValue("id", issued.ID))
		require.NoError(t, h.UpdateAuthKeyAllowedModels(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}

	c, rec = echotest.Request(t, http.MethodPut, "/admin/auth-keys/missing/allowed-models", `{"allowed_models":["openai/*"]}`, echotest.WithPathValue("id", "missing"))
	require.NoError(t, h.UpdateAuthKeyAllowedModels(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
