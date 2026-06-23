package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/virtualmodels"
)

func newVMModelRegistry(t *testing.T) *providers.ModelRegistry {
	t.Helper()
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(mock, "openai", "openai")
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return registry
}

func newVMServiceForRegistry(t *testing.T, registry *providers.ModelRegistry, defaultEnabled bool, items ...virtualmodels.VirtualModel) *virtualmodels.Service {
	t.Helper()
	store := newVMTestStore(items...)
	service, err := virtualmodels.NewService(store, registry, defaultEnabled)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service
}

func TestListModels_IncludesModelAccessState(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, false, virtualmodels.VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team/alpha"},
		Enabled:   true,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := newHandlerContext("/admin/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}

	row := body[0]
	if row.Access.Selector != "openai/gpt-4o" {
		t.Fatalf("row.Access.Selector = %q, want openai/gpt-4o", row.Access.Selector)
	}
	if row.Access.DefaultEnabled {
		t.Fatal("row.Access.DefaultEnabled = true, want false")
	}
	if !row.Access.EffectiveEnabled {
		t.Fatal("row.Access.EffectiveEnabled = false, want true")
	}
	if len(row.Access.UserPaths) != 1 || row.Access.UserPaths[0] != "/team/alpha" {
		t.Fatalf("row.Access.UserPaths = %#v, want [/team/alpha]", row.Access.UserPaths)
	}
	if row.Access.Override == nil || row.Access.Override.Source != "openai/gpt-4o" {
		t.Fatalf("row.Access.Override = %#v, want exact override", row.Access.Override)
	}
}

func TestListModels_DisabledPolicyTurnsModelOff(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source:  "openai/gpt-4o",
		Enabled: false,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := newHandlerContext("/admin/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}
	row := body[0]
	if !row.Access.DefaultEnabled {
		t.Fatal("row.Access.DefaultEnabled = false, want true")
	}
	if row.Access.EffectiveEnabled {
		t.Fatal("row.Access.EffectiveEnabled = true, want false (disabled policy)")
	}
}

func TestListModels_AppliesProviderWideOverrideToConcreteModels(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source:    "openai/",
		UserPaths: []string{"/team/provider"},
		Enabled:   true,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := newHandlerContext("/admin/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}
	row := body[0]
	if row.Access.Selector != "openai/gpt-4o" {
		t.Fatalf("row.Access.Selector = %q, want openai/gpt-4o", row.Access.Selector)
	}
	if len(row.Access.UserPaths) != 1 || row.Access.UserPaths[0] != "/team/provider" {
		t.Fatalf("row.Access.UserPaths = %#v, want [/team/provider]", row.Access.UserPaths)
	}
	if row.Access.Override != nil {
		t.Fatalf("row.Access.Override = %#v, want nil for provider-wide override", row.Access.Override)
	}
}

func TestListModels_AppliesGlobalOverrideToConcreteModels(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source:    "/",
		UserPaths: []string{"/team/global"},
		Enabled:   true,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := newHandlerContext("/admin/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}
	row := body[0]
	if len(row.Access.UserPaths) != 1 || row.Access.UserPaths[0] != "/team/global" {
		t.Fatalf("row.Access.UserPaths = %#v, want [/team/global]", row.Access.UserPaths)
	}
	if row.Access.Override != nil {
		t.Fatalf("row.Access.Override = %#v, want nil for global override", row.Access.Override)
	}
}
