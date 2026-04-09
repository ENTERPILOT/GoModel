package modeloverrides

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gomodel/internal/core"
)

type testStore struct {
	items map[string]Override
}

func newTestStore(items ...Override) *testStore {
	store := &testStore{items: make(map[string]Override, len(items))}
	for _, item := range items {
		store.items[item.Selector] = item
	}
	return store
}

func (s *testStore) List(_ context.Context) ([]Override, error) {
	result := make([]Override, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *testStore) Upsert(_ context.Context, override Override) error {
	s.items[override.Selector] = override
	return nil
}

func (s *testStore) Delete(_ context.Context, selector string) error {
	if _, ok := s.items[selector]; !ok {
		return ErrNotFound
	}
	delete(s.items, selector)
	return nil
}

func (s *testStore) Close() error { return nil }

type flakyListStore struct {
	*testStore
	listErr error
}

func newFlakyListStore(items ...Override) *flakyListStore {
	return &flakyListStore{testStore: newTestStore(items...)}
}

func (s *flakyListStore) List(ctx context.Context) ([]Override, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.testStore.List(ctx)
}

type testCatalog struct {
	providerNames []string
}

func (c testCatalog) ProviderNames() []string {
	return append([]string(nil), c.providerNames...)
}

func boolPtr(value bool) *bool {
	return &value
}

func TestNormalizeSelectorInput_UsesFirstSlashOnlyForKnownProviders(t *testing.T) {
	providerNames := []string{"openai", "anthropic"}

	t.Run("known provider prefix becomes provider selector", func(t *testing.T) {
		selector, providerName, model, err := normalizeSelectorInput(providerNames, "openai/gpt-4o")
		if err != nil {
			t.Fatalf("normalizeSelectorInput() error = %v", err)
		}
		if selector != "openai/gpt-4o" || providerName != "openai" || model != "gpt-4o" {
			t.Fatalf("normalizeSelectorInput() = (%q, %q, %q), want (%q, %q, %q)", selector, providerName, model, "openai/gpt-4o", "openai", "gpt-4o")
		}
	})

	t.Run("unknown provider prefix stays in raw model id", func(t *testing.T) {
		selector, providerName, model, err := normalizeSelectorInput(providerNames, "vendor/model-with-slash")
		if err != nil {
			t.Fatalf("normalizeSelectorInput() error = %v", err)
		}
		if selector != "vendor/model-with-slash" || providerName != "" || model != "vendor/model-with-slash" {
			t.Fatalf("normalizeSelectorInput() = (%q, %q, %q), want (%q, %q, %q)", selector, providerName, model, "vendor/model-with-slash", "", "vendor/model-with-slash")
		}
	})

	t.Run("provider-wide selector keeps empty model", func(t *testing.T) {
		selector, providerName, model, err := normalizeSelectorInput(providerNames, "anthropic/")
		if err != nil {
			t.Fatalf("normalizeSelectorInput() error = %v", err)
		}
		if selector != "anthropic/" || providerName != "anthropic" || model != "" {
			t.Fatalf("normalizeSelectorInput() = (%q, %q, %q), want (%q, %q, %q)", selector, providerName, model, "anthropic/", "anthropic", "")
		}
	})
}

func TestService_DefaultDisabledRequiresExplicitEnableAndHonorsUserPaths(t *testing.T) {
	service, err := NewService(
		newTestStore(Override{
			Selector:                "openai/gpt-4o",
			Enabled:                 boolPtr(true),
			AllowedOnlyForUserPaths: []string{"/team/alpha"},
		}),
		testCatalog{providerNames: []string{"openai"}},
		false,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	enabledSelector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	disabledSelector := core.ModelSelector{Provider: "openai", Model: "gpt-5"}

	state := service.EffectiveState(enabledSelector)
	if !state.Enabled {
		t.Fatal("EffectiveState().Enabled = false, want true")
	}
	if !state.DefaultEnabled {
		// false is expected; explicit assertion below for clarity.
	} else {
		t.Fatal("EffectiveState().DefaultEnabled = true, want false")
	}
	if len(state.AllowedOnlyForUserPaths) != 1 || state.AllowedOnlyForUserPaths[0] != "/team/alpha" {
		t.Fatalf("EffectiveState().AllowedOnlyForUserPaths = %#v, want [/team/alpha]", state.AllowedOnlyForUserPaths)
	}

	allowedCtx := core.WithEffectiveUserPath(context.Background(), "/team/alpha/project-x")
	if !service.AllowsModel(allowedCtx, enabledSelector) {
		t.Fatal("AllowsModel() = false, want true for descendant user path")
	}
	if err := service.ValidateModelAccess(allowedCtx, enabledSelector); err != nil {
		t.Fatalf("ValidateModelAccess() error = %v, want nil", err)
	}

	deniedCtx := core.WithEffectiveUserPath(context.Background(), "/team/beta")
	if service.AllowsModel(deniedCtx, enabledSelector) {
		t.Fatal("AllowsModel() = true, want false for mismatched user path")
	}
	err = service.ValidateModelAccess(deniedCtx, enabledSelector)
	if err == nil {
		t.Fatal("ValidateModelAccess() error = nil, want access denial")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("ValidateModelAccess() error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.StatusCode != http.StatusBadRequest || gatewayErr.Code == nil || *gatewayErr.Code != "model_access_denied" {
		t.Fatalf("ValidateModelAccess() = status %d code %#v, want 400/model_access_denied", gatewayErr.StatusCode, gatewayErr.Code)
	}

	if service.AllowsModel(allowedCtx, disabledSelector) {
		t.Fatal("AllowsModel() = true, want false for model without explicit enable when defaults are disabled")
	}
}

func TestService_ForceDisabledOverridesBroaderEnable(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openai/", Enabled: boolPtr(true)},
			Override{Selector: "openai/gpt-4o", ForceDisabled: true},
		),
		testCatalog{providerNames: []string{"openai"}},
		false,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	blocked := service.EffectiveState(core.ModelSelector{Provider: "openai", Model: "gpt-4o"})
	if blocked.Enabled {
		t.Fatal("EffectiveState().Enabled = true, want false when exact force_disabled applies")
	}
	if !blocked.ForceDisabled {
		t.Fatal("EffectiveState().ForceDisabled = false, want true")
	}

	allowed := service.EffectiveState(core.ModelSelector{Provider: "openai", Model: "gpt-4.1"})
	if !allowed.Enabled {
		t.Fatal("EffectiveState().Enabled = false, want true for provider-wide enable")
	}
	if allowed.ForceDisabled {
		t.Fatal("EffectiveState().ForceDisabled = true, want false")
	}
}

func TestService_ExactEnableClearsBroaderForceDisabled(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openai/", ForceDisabled: true},
			Override{Selector: "openai/gpt-4o", Enabled: boolPtr(true)},
		),
		testCatalog{providerNames: []string{"openai"}},
		true,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	state := service.EffectiveState(core.ModelSelector{Provider: "openai", Model: "gpt-4o"})
	if !state.Enabled {
		t.Fatal("EffectiveState().Enabled = false, want true when exact enable overrides broader force_disabled")
	}
	if state.ForceDisabled {
		t.Fatal("EffectiveState().ForceDisabled = true, want false after exact enable override")
	}
	if err := service.ValidateModelAccess(context.Background(), core.ModelSelector{Provider: "openai", Model: "gpt-4o"}); err != nil {
		t.Fatalf("ValidateModelAccess() error = %v, want nil", err)
	}
}

func TestService_UpsertRollsBackStorageOnRefreshFailure(t *testing.T) {
	store := newFlakyListStore(
		Override{Selector: "openai/gpt-4o", Enabled: boolPtr(true)},
	)
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.listErr = errors.New("list failed")
	err = service.Upsert(context.Background(), Override{Selector: "openai/gpt-5", Enabled: boolPtr(true)})
	if err == nil {
		t.Fatal("Upsert() error = nil, want refresh failure")
	}
	if _, ok := store.items["openai/gpt-5"]; ok {
		t.Fatal("store mutated after failed refresh; expected rollback to remove openai/gpt-5")
	}
	if _, ok := service.Get("openai/gpt-5"); ok {
		t.Fatal("service cache mutated after failed refresh; expected openai/gpt-5 to remain absent")
	}
}

func TestService_DeleteRollsBackStorageOnRefreshFailure(t *testing.T) {
	store := newFlakyListStore(
		Override{Selector: "openai/gpt-4o", Enabled: boolPtr(true)},
	)
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.listErr = errors.New("list failed")
	err = service.Delete(context.Background(), "openai/gpt-4o")
	if err == nil {
		t.Fatal("Delete() error = nil, want refresh failure")
	}
	if _, ok := store.items["openai/gpt-4o"]; !ok {
		t.Fatal("store lost openai/gpt-4o after failed refresh; expected rollback to restore it")
	}
	if _, ok := service.Get("openai/gpt-4o"); !ok {
		t.Fatal("service cache lost openai/gpt-4o after failed refresh")
	}
}
