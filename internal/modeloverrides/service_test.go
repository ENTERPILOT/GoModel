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

	t.Run("slash selector becomes global scope", func(t *testing.T) {
		selector, providerName, model, err := normalizeSelectorInput(providerNames, "/")
		if err != nil {
			t.Fatalf("normalizeSelectorInput() error = %v", err)
		}
		if selector != "/" || providerName != "" || model != "" {
			t.Fatalf("normalizeSelectorInput() = (%q, %q, %q), want (%q, %q, %q)", selector, providerName, model, "/", "", "")
		}
	})
}

func TestService_DefaultEnabledRestrictsMatchingOverrideByUserPath(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openai/gpt-4o", UserPaths: []string{"/team/alpha"}},
			Override{Selector: "openai/gpt-5", UserPaths: []string{"/non-existing"}},
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

	restrictedSelector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	state := service.EffectiveState(restrictedSelector)
	if !state.Enabled {
		t.Fatal("EffectiveState().Enabled = false, want true")
	}
	if !state.DefaultEnabled {
		t.Fatal("EffectiveState().DefaultEnabled = false, want true")
	}
	if len(state.UserPaths) != 1 || state.UserPaths[0] != "/team/alpha" {
		t.Fatalf("EffectiveState().UserPaths = %#v, want [/team/alpha]", state.UserPaths)
	}

	allowedCtx := core.WithEffectiveUserPath(context.Background(), "/team/alpha/project-x")
	if !service.AllowsModel(allowedCtx, restrictedSelector) {
		t.Fatal("AllowsModel() = false, want true for descendant user path")
	}
	if err := service.ValidateModelAccess(allowedCtx, restrictedSelector); err != nil {
		t.Fatalf("ValidateModelAccess() error = %v, want nil", err)
	}

	deniedCtx := core.WithEffectiveUserPath(context.Background(), "/team/beta")
	if service.AllowsModel(deniedCtx, restrictedSelector) {
		t.Fatal("AllowsModel() = true, want false for mismatched user path")
	}
	err = service.ValidateModelAccess(deniedCtx, restrictedSelector)
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

	if service.AllowsModel(allowedCtx, core.ModelSelector{Provider: "openai", Model: "gpt-5"}) {
		t.Fatal("AllowsModel() = true, want false for non-existing sentinel path")
	}
	if !service.AllowsModel(context.Background(), core.ModelSelector{Provider: "openai", Model: "gpt-4.1"}) {
		t.Fatal("AllowsModel() = false, want true for model without override when defaults are enabled")
	}
}

func TestService_DefaultDisabledAllowsRootAndSpecificUserPathOverrides(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "openai/gpt-4o", UserPaths: []string{"/"}},
			Override{Selector: "openai/gpt-5", UserPaths: []string{"/team/alpha"}},
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

	rootAllowedSelector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	if !service.AllowsModel(context.Background(), rootAllowedSelector) {
		t.Fatal("AllowsModel() = false, want true for root user_path override without request user path")
	}
	if !service.AllowsModel(core.WithEffectiveUserPath(context.Background(), "/team/beta"), rootAllowedSelector) {
		t.Fatal("AllowsModel() = false, want true for root user_path override with any request user path")
	}

	teamSelector := core.ModelSelector{Provider: "openai", Model: "gpt-5"}
	if !service.AllowsModel(core.WithEffectiveUserPath(context.Background(), "/team/alpha/service"), teamSelector) {
		t.Fatal("AllowsModel() = false, want true for matching subtree")
	}
	if service.AllowsModel(core.WithEffectiveUserPath(context.Background(), "/team/beta"), teamSelector) {
		t.Fatal("AllowsModel() = true, want false for mismatched subtree")
	}
	if service.AllowsModel(context.Background(), teamSelector) {
		t.Fatal("AllowsModel() = true, want false without request user path")
	}

	if service.AllowsModel(context.Background(), core.ModelSelector{Provider: "openai", Model: "gpt-4.1"}) {
		t.Fatal("AllowsModel() = true, want false for model without override when defaults are disabled")
	}
}

func TestService_MostSpecificOverrideWins(t *testing.T) {
	service, err := NewService(
		newTestStore(
			Override{Selector: "/", UserPaths: []string{"/team/global"}},
			Override{Selector: "gpt-4o", UserPaths: []string{"/team/model"}},
			Override{Selector: "openai/", UserPaths: []string{"/team/provider"}},
			Override{Selector: "openai/gpt-4o", UserPaths: []string{"/team/exact"}},
		),
		testCatalog{providerNames: []string{"openai", "anthropic"}},
		true,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	tests := []struct {
		name     string
		selector core.ModelSelector
		wantPath string
	}{
		{
			name:     "exact provider model wins",
			selector: core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
			wantPath: "/team/exact",
		},
		{
			name:     "provider-wide beats global",
			selector: core.ModelSelector{Provider: "openai", Model: "gpt-4.1"},
			wantPath: "/team/provider",
		},
		{
			name:     "model-wide beats global",
			selector: core.ModelSelector{Provider: "anthropic", Model: "gpt-4o"},
			wantPath: "/team/model",
		},
		{
			name:     "global applies last",
			selector: core.ModelSelector{Provider: "anthropic", Model: "claude-3-7-sonnet"},
			wantPath: "/team/global",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := service.EffectiveState(tt.selector)
			if len(state.UserPaths) != 1 || state.UserPaths[0] != tt.wantPath {
				t.Fatalf("EffectiveState().UserPaths = %#v, want [%s]", state.UserPaths, tt.wantPath)
			}
		})
	}
}

func TestService_UpsertRejectsEmptyUserPaths(t *testing.T) {
	service, err := NewService(newTestStore(), testCatalog{providerNames: []string{"openai"}}, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = service.Upsert(context.Background(), Override{Selector: "openai/gpt-4o"})
	if err == nil {
		t.Fatal("Upsert() error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("Upsert() error type = %T, want validation error", err)
	}
}

func TestService_UpsertRollsBackStorageOnRefreshFailure(t *testing.T) {
	store := newFlakyListStore(
		Override{Selector: "openai/gpt-4o", UserPaths: []string{"/"}},
	)
	service, err := NewService(store, testCatalog{providerNames: []string{"openai"}}, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	store.listErr = errors.New("list failed")
	err = service.Upsert(context.Background(), Override{Selector: "openai/gpt-5", UserPaths: []string{"/"}})
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
		Override{Selector: "openai/gpt-4o", UserPaths: []string{"/"}},
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
