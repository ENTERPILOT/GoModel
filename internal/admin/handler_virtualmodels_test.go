package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// vmTestStore is an in-memory virtualmodels.Store for admin handler tests.
type vmTestStore struct {
	items map[string]virtualmodels.VirtualModel
}

func newVMTestStore(items ...virtualmodels.VirtualModel) *vmTestStore {
	store := &vmTestStore{items: make(map[string]virtualmodels.VirtualModel, len(items))}
	for _, item := range items {
		store.items[item.Source] = item
	}
	return store
}

func (s *vmTestStore) List(_ context.Context) ([]virtualmodels.VirtualModel, error) {
	result := make([]virtualmodels.VirtualModel, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *vmTestStore) Get(_ context.Context, source string) (*virtualmodels.VirtualModel, error) {
	item, ok := s.items[source]
	if !ok {
		return nil, virtualmodels.ErrNotFound
	}
	clone := item
	return &clone, nil
}

func (s *vmTestStore) Upsert(_ context.Context, vm virtualmodels.VirtualModel) error {
	s.items[vm.Source] = vm
	return nil
}

func (s *vmTestStore) Delete(_ context.Context, source string) error {
	if _, ok := s.items[source]; !ok {
		return virtualmodels.ErrNotFound
	}
	delete(s.items, source)
	return nil
}

func (s *vmTestStore) Close() error { return nil }

type failingVMStore struct {
	listErr   error
	upsertErr error
	deleteErr error
}

func (s *failingVMStore) List(_ context.Context) ([]virtualmodels.VirtualModel, error) {
	return nil, s.listErr
}
func (s *failingVMStore) Get(_ context.Context, _ string) (*virtualmodels.VirtualModel, error) {
	return nil, virtualmodels.ErrNotFound
}
func (s *failingVMStore) Upsert(_ context.Context, _ virtualmodels.VirtualModel) error {
	return s.upsertErr
}
func (s *failingVMStore) Delete(_ context.Context, _ string) error { return s.deleteErr }
func (s *failingVMStore) Close() error                             { return nil }

type vmTestCatalog struct {
	providerTypes map[string]string
	models        map[string]core.Model
}

func newVMTestCatalog() *vmTestCatalog {
	return &vmTestCatalog{
		providerTypes: map[string]string{},
		models:        map[string]core.Model{},
	}
}

func (c *vmTestCatalog) add(model, providerType string) {
	c.providerTypes[model] = providerType
	c.models[model] = core.Model{ID: model, Object: "model"}
}

func (c *vmTestCatalog) Supports(model string) bool {
	_, ok := c.models[model]
	return ok
}

func (c *vmTestCatalog) ModelAvailable(model string) bool {
	return c.Supports(model)
}

func (c *vmTestCatalog) GetProviderType(model string) string {
	return c.providerTypes[model]
}

func (c *vmTestCatalog) LookupModel(model string) (*core.Model, bool) {
	value, ok := c.models[model]
	if !ok {
		return nil, false
	}
	clone := value
	return &clone, true
}

func (c *vmTestCatalog) ProviderNames() []string {
	seen := map[string]struct{}{}
	names := make([]string, 0)
	for _, providerType := range c.providerTypes {
		if providerType == "" {
			continue
		}
		if _, ok := seen[providerType]; ok {
			continue
		}
		seen[providerType] = struct{}{}
		names = append(names, providerType)
	}
	return names
}

func newVMService(t *testing.T, catalog *vmTestCatalog, store virtualmodels.Store, defaultEnabled bool) *virtualmodels.Service {
	t.Helper()
	service, err := virtualmodels.NewService(store, catalog, defaultEnabled)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return service
}

func newVMHandler(t *testing.T, items ...virtualmodels.VirtualModel) *Handler {
	t.Helper()
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("openai/gpt-4o-mini", "openai")
	service := newVMService(t, catalog, newVMTestStore(items...), true)
	return NewHandler(nil, nil, WithVirtualModels(service))
}

func redirectVM(source, target string, enabled bool) virtualmodels.VirtualModel {
	selector, _ := core.ParseModelSelector(target, "")
	return virtualmodels.VirtualModel{
		Source:  source,
		Targets: []virtualmodels.Target{{Provider: selector.Provider, Model: selector.Model}},
		Enabled: enabled,
	}
}

func TestListVirtualModels_RedirectAndPolicy(t *testing.T) {
	h := newVMHandler(t,
		redirectVM("smart", "openai/gpt-4o", true),
		virtualmodels.VirtualModel{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/team"}, Enabled: true},
	)
	c, rec := echotest.Get(t, "/admin/virtual-models")
	err := h.ListVirtualModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]virtualmodels.View](t, rec)
	require.Len(t, body, 2)

	kinds := map[string]virtualmodels.View{}
	for _, v := range body {
		kinds[v.Source] = v
	}
	assert.Equal(t, virtualmodels.KindRedirect, kinds["smart"].Kind)
	assert.True(t, kinds["smart"].Valid)
	assert.Equal(t, virtualmodels.KindPolicy, kinds["openai/gpt-4o"].Kind)
}

func TestVirtualModelEndpointsReturn503WhenUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)

	assertUnavailable := func(name string, err error, rec *httptest.ResponseRecorder) {
		t.Helper()
		require.NoError(t, err)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
		body := echotest.Decode[map[string]map[string]any](t, rec)
		assert.Equal(t, "feature_unavailable", body["error"]["code"], name)
	}

	listCtx, listRec := echotest.Get(t, "/admin/virtual-models")
	assertUnavailable("ListVirtualModels", h.ListVirtualModels(listCtx), listRec)

	putCtx, putRec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","target_model":"openai/gpt-4o"}`)
	assertUnavailable("UpsertVirtualModel", h.UpsertVirtualModel(putCtx), putRec)

	deleteCtx, deleteRec := echotest.Request(t, http.MethodDelete, "/admin/virtual-models", `{"source":"smart"}`)
	assertUnavailable("DeleteVirtualModel", h.DeleteVirtualModel(deleteCtx), deleteRec)
}

func TestUpsertAndDeleteRedirectVirtualModel(t *testing.T) {
	h := newVMHandler(t)

	putCtx, putRec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","target_model":"openai/gpt-4o","description":"primary"}`)
	err := h.UpsertVirtualModel(putCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, putRec)
	assert.Equal(t, "smart", view.Source)
	assert.Equal(t, virtualmodels.KindRedirect, view.Kind)

	deleteCtx, deleteRec := echotest.Request(t, http.MethodDelete, "/admin/virtual-models", `{"source":"smart"}`)
	err = h.DeleteVirtualModel(deleteCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, deleteRec.Code)
}

func TestUpsertVirtualModelRemovesRedirectAndPreservesPolicyFields(t *testing.T) {
	h := newVMHandler(t)

	putCtx, _ := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"gpt-4o","target_model":"openai/gpt-4o","description":"Team model","user_paths":["/team"],"enabled":true}`)
	err := h.UpsertVirtualModel(putCtx)
	require.NoError(t, err)

	policyCtx, policyRec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"gpt-4o","description":"Team model","user_paths":["/team"],"enabled":true}`)
	err = h.UpsertVirtualModel(policyCtx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, policyRec.Code, policyRec.Body.String())

	vm, ok := h.virtualModels.Get("gpt-4o")
	require.True(t, ok)
	assert.False(t, vm.IsRedirect(), "policy replacement left targets %#v", vm.Targets)
	assert.Equal(t, "Team model", vm.Description)
	assert.True(t, vm.Enabled)
	assert.Equal(t, []string{"/team"}, vm.UserPaths)
}

func TestUpsertVirtualModelEmptyEditDropsNoopRecord(t *testing.T) {
	h := newVMHandler(t, redirectVM("gpt-4o", "openai/gpt-4o", true))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"gpt-4o","enabled":true}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	_, ok := h.virtualModels.Get("gpt-4o")
	assert.False(t, ok)
}

func TestUpsertVirtualModelRenamesViaOldSource(t *testing.T) {
	h := newVMHandler(t, redirectVM("smart", "openai/gpt-4o", false))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smarter","old_source":"smart","target_model":"openai/gpt-4o"}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.Equal(t, "smarter", view.Source)
	assert.Equal(t, virtualmodels.KindRedirect, view.Kind)
	// The omitted enabled flag is carried over from the old row (disabled).
	assert.False(t, view.Enabled, "rename flipped a disabled redirect to enabled")

	// The old source no longer exists.
	_, ok := h.virtualModels.Get("smart")
	assert.False(t, ok)
}

func TestUpsertVirtualModelRejectsRenameOntoExisting(t *testing.T) {
	h := newVMHandler(t, redirectVM("smart", "openai/gpt-4o", true), redirectVM("taken", "openai/gpt-4o", true))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"taken","old_source":"smart","target_model":"openai/gpt-4o"}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// Both rows survive the rejected rename.
	_, ok := h.virtualModels.Get("smart")
	assert.True(t, ok)
	_, ok = h.virtualModels.Get("taken")
	assert.True(t, ok)
}

func TestUpsertPolicyVirtualModelAcceptsEmptyUserPaths(t *testing.T) {
	h := newVMHandler(t)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"openai/gpt-4o","enabled":false}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.Equal(t, virtualmodels.KindPolicy, view.Kind)
	assert.False(t, view.Enabled)
}

func TestUpsertVirtualModelValidatesSlowdown(t *testing.T) {
	tests := []struct {
		name       string
		slowdown   float64
		wantStatus int
	}{
		{name: "explicit zero", slowdown: 0, wantStatus: http.StatusOK},
		{name: "minimum", slowdown: 0.1, wantStatus: http.StatusOK},
		{name: "maximum", slowdown: 10, wantStatus: http.StatusOK},
		{name: "below minimum", slowdown: 0.09, wantStatus: http.StatusBadRequest},
		{name: "above maximum", slowdown: 10.1, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newVMHandler(t)
			body := fmt.Sprintf(`{"source":"slow","target_model":"openai/gpt-4o","slowdown":%v}`, tt.slowdown)
			c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", body)
			err := h.UpsertVirtualModel(c)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				return
			}

			view := echotest.Decode[virtualmodels.View](t, rec)
			require.NotNil(t, view.Slowdown)
			assert.InDelta(t, tt.slowdown, *view.Slowdown, 0)
		})
	}
}

func TestUpsertRedirectVirtualModelReplacesAccessPolicy(t *testing.T) {
	h := newVMHandler(t, virtualmodels.VirtualModel{
		Source:       "openai/gpt-4o",
		ProviderName: "openai",
		Model:        "gpt-4o",
		UserPaths:    []string{"/team"},
		Enabled:      true,
	})

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"openai/gpt-4o","target_model":"openai/gpt-4o-mini","description":"fallback","enabled":true}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.Equal(t, virtualmodels.KindRedirect, view.Kind)
	assert.Empty(t, view.UserPaths)
	assert.Equal(t, []virtualmodels.Target{{Model: "openai/gpt-4o-mini"}}, view.Targets, "target must keep the bare name")
}

// A target written as one name is kept by name, so it may chain to a virtual
// model that looks like provider/model; an explicit provider pins a concrete
// model and is validated as such.
func TestUpsertVirtualModelBareTargetChainsToSlashNamedVirtualModel(t *testing.T) {
	h := newVMHandler(t, redirectVM("team/cheap", "openai/gpt-4o", true))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"outer","target_model":"team/cheap","enabled":true}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.Equal(t, []virtualmodels.Target{{Model: "team/cheap"}}, view.Targets)
	assert.Equal(t, "openai/gpt-4o", view.ResolvedModel)

	c, rec = echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"pinned","targets":[{"provider":"team","model":"cheap"}],"enabled":true}`)
	err = h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, "an explicit provider that does not exist must be rejected: %s", rec.Body.String())
}

func TestUpsertVirtualModelPreservesEnabledWhenOmitted(t *testing.T) {
	h := newVMHandler(t, redirectVM("smart", "openai/gpt-4o", false))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","target_model":"openai/gpt-4o","description":"after"}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.False(t, view.Enabled)
	assert.Equal(t, "after", view.Description)
}

func TestUpsertVirtualModelLoadBalanced(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("groq/llama", "groq")
	service := newVMService(t, catalog, newVMTestStore(), true)
	h := NewHandler(nil, nil, WithVirtualModels(service))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","strategy":"cost","targets":[{"model":"openai/gpt-4o"},{"model":"groq/llama","weight":2}]}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	view := echotest.Decode[virtualmodels.View](t, rec)
	assert.Equal(t, virtualmodels.KindRedirect, view.Kind)
	assert.Equal(t, virtualmodels.StrategyCost, view.Strategy)
	require.Len(t, view.Targets, 2)
	assert.InDelta(t, 2, view.Targets[1].Weight, 0)
}

func TestUpsertVirtualModelRejectsBlankTargets(t *testing.T) {
	h := newVMHandler(t)

	// A targets list whose only entry has an empty model must not be silently
	// demoted to an access policy.
	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","targets":[{"model":"  "}]}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_request_error")
}

func TestUpsertVirtualModelReturns400OnValidationError(t *testing.T) {
	h := newVMHandler(t)

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","target_model":"openai/missing"}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "invalid_request_error")
}

func TestUpsertVirtualModelBubblesProviderErrorOnStoreFailure(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	service := newVMService(t, catalog, &failingVMStore{upsertErr: errors.New("disk full")}, true)
	h := NewHandler(nil, nil, WithVirtualModels(service))

	c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", `{"source":"smart","target_model":"openai/gpt-4o"}`)
	err := h.UpsertVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

func TestDeleteVirtualModelNotFound(t *testing.T) {
	h := newVMHandler(t)

	c, rec := echotest.Request(t, http.MethodDelete, "/admin/virtual-models", `{"source":"missing"}`)
	err := h.DeleteVirtualModel(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestUpsertVirtualModelTargetOrderRoundTrips pins the contract the
// dashboard's drag-to-reorder relies on: the targets array travels in display
// order (failover primary first), and a reorder save stores and returns the
// targets in exactly the order the editor sent them.
func TestUpsertVirtualModelTargetOrderRoundTrips(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("openai/gpt-4o-mini", "openai")
	catalog.add("anthropic/claude-haiku", "anthropic")
	service := newVMService(t, catalog, newVMTestStore(redirectVM("smart", "openai/gpt-4o", true)), true)
	h := NewHandler(nil, nil, WithVirtualModels(service))

	put := func(body string) {
		t.Helper()
		c, rec := echotest.Request(t, http.MethodPut, "/admin/virtual-models", body)
		err := h.UpsertVirtualModel(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	// Initial order: gpt-4o is the failover primary.
	put(`{"source":"smart","strategy":"failover","targets":[{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"},{"model":"anthropic/claude-haiku"}]}`)

	vm, ok := service.Get("smart")
	require.True(t, ok)
	assert.Equal(t, []string{"openai/gpt-4o", "openai/gpt-4o-mini", "anthropic/claude-haiku"}, qualifiedTargetNames(vm.Targets))

	// Reorder save: what the editor sends after dragging the last target onto
	// the first row. The new primary must land first, the rest keep their
	// relative order.
	put(`{"source":"smart","strategy":"failover","targets":[{"model":"anthropic/claude-haiku"},{"model":"openai/gpt-4o"},{"model":"openai/gpt-4o-mini"}]}`)

	vm, ok = service.Get("smart")
	require.True(t, ok)
	want := []string{"anthropic/claude-haiku", "openai/gpt-4o", "openai/gpt-4o-mini"}
	assert.Equal(t, want, qualifiedTargetNames(vm.Targets), "stored order after reorder")

	// The list view the dashboard renders must return the same order.
	c, rec := echotest.Get(t, "/admin/virtual-models")
	err := h.ListVirtualModels(c)
	require.NoError(t, err)

	views := echotest.Decode[[]virtualmodels.View](t, rec)
	idx := slices.IndexFunc(views, func(view virtualmodels.View) bool { return view.Source == "smart" })
	require.NotEqual(t, -1, idx, "smart missing from list views")
	assert.Equal(t, want, qualifiedTargetNames(views[idx].Targets), "view order after reorder")
}

func qualifiedTargetNames(targets []virtualmodels.Target) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Provider != "" {
			names = append(names, target.Provider+"/"+target.Model)
			continue
		}
		names = append(names, target.Model)
	}
	return names
}
