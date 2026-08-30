package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/authkeys"
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
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	userService, err := users.NewService(store, userTestCatalog{"openai", "anthropic"})
	if err != nil {
		t.Fatalf("users.NewService: %v", err)
	}
	if err := userService.Refresh(ctx); err != nil {
		t.Fatalf("users.Refresh: %v", err)
	}
	keyService, err := authkeys.NewService(newAuthKeyTestStore(keys...))
	if err != nil {
		t.Fatalf("authkeys.NewService: %v", err)
	}
	if err := keyService.Refresh(ctx); err != nil {
		t.Fatalf("authkeys.Refresh: %v", err)
	}
	return NewHandler(nil, nil, WithUsers(userService), WithAuthKeys(keyService))
}

func jsonRequest(method, target, body string) (*echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func decodeUsers(t *testing.T, rec *httptest.ResponseRecorder) map[string]userNodeResponse {
	t.Helper()
	var resp userListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal users response: %v (%s)", err, rec.Body.String())
	}
	byPath := make(map[string]userNodeResponse, len(resp.Users))
	for i, node := range resp.Users {
		if i > 0 && resp.Users[i-1].UserPath > node.UserPath {
			t.Fatalf("users not sorted by path: %v", resp.Users)
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
		ctx  *echo.Context
		rec  *httptest.ResponseRecorder
	}{
		{name: "list", run: h.ListUsers},
		{name: "upsert", run: h.UpsertUser},
		{name: "delete", run: h.DeleteUser},
	} {
		c, rec := jsonRequest(http.MethodGet, "/admin/users", "")
		if err := tc.run(c); err != nil {
			t.Fatalf("%s error = %v", tc.name, err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", tc.name, rec.Code)
		}
	}
}

func TestUsersTreeDerivesFromPoliciesAndKeys(t *testing.T) {
	now := time.Now().UTC()
	h := newUsersHandler(t,
		authkeys.AuthKey{ID: "k1", Name: "alice", UserPath: "/acme/eng/alice", SecretHash: "h1", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "k2", Name: "alice-2", UserPath: "/acme/eng/alice", SecretHash: "h2", Enabled: true, CreatedAt: now, UpdatedAt: now},
		authkeys.AuthKey{ID: "k3", Name: "ops", UserPath: "/acme/ops", SecretHash: "h3", Enabled: true, CreatedAt: now, UpdatedAt: now},
	)

	c, rec := jsonRequest(http.MethodPut, "/admin/users", `{"user_path":"acme/eng","allowed_models":["anthropic/*"," "],"description":"eng"}`)
	if err := h.UpsertUser(c); err != nil {
		t.Fatalf("UpsertUser error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("UpsertUser status = %d: %s", rec.Code, rec.Body.String())
	}
	c, rec = jsonRequest(http.MethodPut, "/admin/users", `{"user_path":"/acme","allowed_models":["openai/*","anthropic/*"]}`)
	if err := h.UpsertUser(c); err != nil {
		t.Fatalf("UpsertUser(/acme) error = %v", err)
	}
	nodes := decodeUsers(t, rec)

	for _, path := range []string{"/", "/acme", "/acme/eng", "/acme/eng/alice", "/acme/ops"} {
		if _, ok := nodes[path]; !ok {
			t.Fatalf("node %s missing from %v", path, nodes)
		}
	}
	if len(nodes) != 5 {
		t.Fatalf("got %d nodes, want 5: %v", len(nodes), nodes)
	}
	eng := nodes["/acme/eng"]
	if !eng.Configured || eng.Description != "eng" || !reflect.DeepEqual(eng.AllowedModels, []string{"anthropic/"}) || eng.KeyCount != 0 {
		t.Fatalf("/acme/eng = %#v", eng)
	}
	if !reflect.DeepEqual(eng.InheritedFrom, []string{"/acme"}) {
		t.Fatalf("/acme/eng inherited_from = %v, want [/acme]", eng.InheritedFrom)
	}
	alice := nodes["/acme/eng/alice"]
	if alice.Configured || alice.KeyCount != 2 || len(alice.AllowedModels) != 0 || !reflect.DeepEqual(alice.InheritedFrom, []string{"/acme", "/acme/eng"}) {
		t.Fatalf("/acme/eng/alice = %#v", alice)
	}
	if ops := nodes["/acme/ops"]; ops.KeyCount != 1 || !reflect.DeepEqual(ops.InheritedFrom, []string{"/acme"}) {
		t.Fatalf("/acme/ops = %#v", ops)
	}
	if root := nodes["/"]; root.Configured || len(root.InheritedFrom) != 0 {
		t.Fatalf("/ = %#v", root)
	}

	c, rec = jsonRequest(http.MethodDelete, "/admin/users?user_path=/acme/eng", "")
	if err := h.DeleteUser(c); err != nil {
		t.Fatalf("DeleteUser error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("DeleteUser status = %d: %s", rec.Code, rec.Body.String())
	}
	nodes = decodeUsers(t, rec)
	if eng, ok := nodes["/acme/eng"]; !ok || eng.Configured {
		// The node stays as an implied ancestor of alice's keys.
		t.Fatalf("/acme/eng after delete = %#v (present=%v)", eng, ok)
	}

	c, rec = jsonRequest(http.MethodDelete, "/admin/users?user_path=/acme/eng", "")
	if err := h.DeleteUser(c); err != nil {
		t.Fatalf("DeleteUser(missing) error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DeleteUser(missing) status = %d, want 404", rec.Code)
	}
}

func TestUsersTreeReportsEffectiveModels(t *testing.T) {
	ctx := context.Background()
	registry := newVMModelRegistry(t) // openai/gpt-4o only
	store, err := users.NewSQLStore(ctx, sqlxtest.NewSQLite(t))
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	userService, err := users.NewService(store, registry)
	if err != nil {
		t.Fatalf("users.NewService: %v", err)
	}
	if err := userService.Refresh(ctx); err != nil {
		t.Fatalf("users.Refresh: %v", err)
	}
	vmService := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/acme"}, Enabled: true,
	})
	vmService.SetAccessPolicy(userService)
	h := NewHandler(nil, registry, WithUsers(userService), WithVirtualModels(vmService))

	c, _ := jsonRequest(http.MethodPut, "/admin/users", `{"user_path":"/acme/eng","allowed_models":["gpt-4o"]}`)
	if err := h.UpsertUser(c); err != nil {
		t.Fatalf("UpsertUser error = %v", err)
	}
	c, _ = jsonRequest(http.MethodPut, "/admin/users", `{"user_path":"/acme/eng/bob","allowed_models":["openai/gpt-5"]}`)
	if err := h.UpsertUser(c); err != nil {
		t.Fatalf("UpsertUser(bob) error = %v", err)
	}
	c, rec := jsonRequest(http.MethodPut, "/admin/users", `{"user_path":"/other","allowed_models":["openai/*"]}`)
	if err := h.UpsertUser(c); err != nil {
		t.Fatalf("UpsertUser(other) error = %v", err)
	}
	nodes := decodeUsers(t, rec)

	// /acme: unrestricted on the user side, model-side row allows gpt-4o here.
	if n := nodes["/acme"]; n.Restricted || !reflect.DeepEqual(n.EffectiveModels, []string{"openai/gpt-4o"}) {
		t.Fatalf("/acme = %#v", n)
	}
	// /acme/eng: own model-wide selector keeps gpt-4o.
	if n := nodes["/acme/eng"]; !n.Restricted || !reflect.DeepEqual(n.EffectiveModels, []string{"openai/gpt-4o"}) {
		t.Fatalf("/acme/eng = %#v", n)
	}
	// /acme/eng/bob: intersection with eng is empty.
	if n := nodes["/acme/eng/bob"]; !n.Restricted || len(n.EffectiveModels) != 0 || n.EffectiveModels == nil {
		t.Fatalf("/acme/eng/bob = %#v", n)
	}
	// /other: user side allows openai, but the model-side row scopes gpt-4o to /acme.
	if n := nodes["/other"]; !n.Restricted || len(n.EffectiveModels) != 0 {
		t.Fatalf("/other = %#v", n)
	}
}

func TestUpsertUserRejectsUnknownProvider(t *testing.T) {
	h := newUsersHandler(t)
	c, rec := jsonRequest(http.MethodPut, "/admin/users", `{"user_path":"/acme","allowed_models":["nope/*"]}`)
	if err := h.UpsertUser(c); err != nil {
		t.Fatalf("UpsertUser error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("UpsertUser status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthKeyAllowedModelsCreateAndUpdate(t *testing.T) {
	h := newUsersHandler(t)

	c, rec := jsonRequest(http.MethodPost, "/admin/auth-keys", `{"name":"restricted","user_path":"/acme/eng","allowed_models":["anthropic/*","openai/gpt-4o","anthropic/*"]}`)
	if err := h.CreateAuthKey(c); err != nil {
		t.Fatalf("CreateAuthKey error = %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("CreateAuthKey status = %d: %s", rec.Code, rec.Body.String())
	}
	var issued authkeys.IssuedKey
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(issued.AllowedModels, []string{"anthropic/", "openai/gpt-4o"}) {
		t.Fatalf("issued.AllowedModels = %v", issued.AllowedModels)
	}

	c, rec = jsonRequest(http.MethodPost, "/admin/auth-keys", `{"name":"bad","allowed_models":["nope/*"]}`)
	if err := h.CreateAuthKey(c); err != nil {
		t.Fatalf("CreateAuthKey(bad) error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("CreateAuthKey(bad) status = %d, want 400", rec.Code)
	}

	c, rec = jsonRequest(http.MethodPut, "/admin/auth-keys/"+issued.ID+"/allowed-models", `{"allowed_models":["openai/*"]}`)
	c.SetPathValues(echo.PathValues{{Name: "id", Value: issued.ID}})
	if err := h.UpdateAuthKeyAllowedModels(c); err != nil {
		t.Fatalf("UpdateAuthKeyAllowedModels error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateAuthKeyAllowedModels status = %d: %s", rec.Code, rec.Body.String())
	}
	var view authkeys.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(view.AllowedModels, []string{"openai/"}) {
		t.Fatalf("view.AllowedModels = %v, want [openai/]", view.AllowedModels)
	}

	c, rec = jsonRequest(http.MethodPut, "/admin/auth-keys/"+issued.ID+"/allowed-models", `{"allowed_models":[]}`)
	c.SetPathValues(echo.PathValues{{Name: "id", Value: issued.ID}})
	if err := h.UpdateAuthKeyAllowedModels(c); err != nil {
		t.Fatalf("UpdateAuthKeyAllowedModels(clear) error = %v", err)
	}
	var cleared authkeys.View
	if err := json.Unmarshal(rec.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cleared.AllowedModels) != 0 {
		t.Fatalf("cleared view.AllowedModels = %v, want empty", view.AllowedModels)
	}

	for _, body := range []string{`{}`, `{"allowed_models":null}`} {
		c, rec = jsonRequest(http.MethodPut, "/admin/auth-keys/"+issued.ID+"/allowed-models", body)
		c.SetPathValues(echo.PathValues{{Name: "id", Value: issued.ID}})
		if err := h.UpdateAuthKeyAllowedModels(c); err != nil {
			t.Fatalf("UpdateAuthKeyAllowedModels(%s) error = %v", body, err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("UpdateAuthKeyAllowedModels(%s) status = %d, want 400", body, rec.Code)
		}
	}

	c, rec = jsonRequest(http.MethodPut, "/admin/auth-keys/missing/allowed-models", `{"allowed_models":["openai/*"]}`)
	c.SetPathValues(echo.PathValues{{Name: "id", Value: "missing"}})
	if err := h.UpdateAuthKeyAllowedModels(c); err != nil {
		t.Fatalf("UpdateAuthKeyAllowedModels(missing) error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("UpdateAuthKeyAllowedModels(missing) status = %d, want 404", rec.Code)
	}
}
