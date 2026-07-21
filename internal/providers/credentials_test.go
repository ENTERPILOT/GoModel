package providers

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// fakeCredentialStore is an in-memory CredentialStore for CredentialsService tests.
type fakeCredentialStore struct {
	mu   sync.Mutex
	rows map[string]ManagedProviderCredential
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{rows: make(map[string]ManagedProviderCredential)}
}

func (s *fakeCredentialStore) List(context.Context) ([]ManagedProviderCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]ManagedProviderCredential, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *fakeCredentialStore) Get(_ context.Context, name string) (*ManagedProviderCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[name]
	if !ok {
		return nil, ErrCredentialNotFound
	}
	return &row, nil
}

func (s *fakeCredentialStore) Upsert(_ context.Context, cred ManagedProviderCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[cred.Name] = cred
	return nil
}

func (s *fakeCredentialStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[name]; !ok {
		return ErrCredentialNotFound
	}
	delete(s.rows, name)
	return nil
}

func (s *fakeCredentialStore) Close() error { return nil }

func newCredentialsTestFactory(t *testing.T) *ProviderFactory {
	t.Helper()
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, _ ProviderOptions) core.Provider {
			return &registryMockProvider{
				name: cfg.Type,
				modelsResponse: &core.ModelsResponse{
					Object: "list",
					Data:   []core.Model{{ID: "test-model", Object: "model", OwnedBy: "test"}},
				},
			}
		},
	})
	return factory
}

func TestCredentialsService_UpsertRegistersAndRoutesImmediately(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name:    "my-openai",
		Type:    "test",
		APIKeys: []string{"sk-test"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if !registry.Supports("my-openai/test-model") {
		t.Error("Supports(my-openai/test-model) = false, want true immediately after Upsert (no restart required)")
	}
	stored, err := store.Get(ctx, "my-openai")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.Type != "test" {
		t.Errorf("stored.Type = %q, want %q", stored.Type, "test")
	}
}

// TestCredentialsService_UpsertSucceedsWhenTheProviderRejectsTheKey covers the
// everyday case of a typo'd or expired API key: the row is still valid input
// (a non-empty key, a known type), so Upsert must report success and leave
// the provider registered-but-unhealthy, matching how providers.Init treats
// an unavailable declarative provider (it stays registered for later
// refreshes rather than failing startup). Upsert must not conflate "the
// registry's background /models call failed" with "the save failed".
func TestCredentialsService_UpsertSucceedsWhenTheProviderRejectsTheKey(t *testing.T) {
	ctx := t.Context()
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, _ ProviderOptions) core.Provider {
			return &registryMockProvider{name: cfg.Type, err: errors.New("401 unauthorized")}
		},
	})
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name:    "bad-key",
		Type:    "test",
		APIKeys: []string{"sk-wrong"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v, want nil (a provider rejecting the key is a save success, not a save failure)", err)
	}
	if registry.ProviderByName("bad-key") == nil {
		t.Error("ProviderByName(bad-key) = nil, want registered even though model discovery failed")
	}
}

// TestCredentialsService_UpsertKeepsThePreviousProviderLiveWhenTheEditIsUnresolvable
// guards against unregistering a working, already-serving provider before
// the replacement is known to be valid. An edit that breaks resolution (e.g.
// stripping the only API key) must be rejected with the old provider still
// registered and routable, not leave the name unregistered.
func TestCredentialsService_UpsertKeepsThePreviousProviderLiveWhenTheEditIsUnresolvable(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	if err := svc.Upsert(ctx, ManagedProviderCredential{Name: "flaky", Type: "test", APIKeys: []string{"sk-good"}, Enabled: true}); err != nil {
		t.Fatalf("initial Upsert() error = %v", err)
	}
	if !registry.Supports("flaky/test-model") {
		t.Fatal("provider did not register on the initial (valid) Upsert")
	}

	// An edit that strips the only API key: the "test" registration doesn't
	// allow keyless credentials, so this must fail to resolve.
	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "flaky", Type: "test", Enabled: true})
	if err == nil {
		t.Fatal("Upsert() with an unresolvable edit error = nil, want an error")
	}

	if registry.ProviderByName("flaky") == nil {
		t.Error("ProviderByName(flaky) = nil after a failed edit, want the previous working provider still registered")
	}
	if !registry.Supports("flaky/test-model") {
		t.Error("Supports(flaky/test-model) = false after a failed edit, want the previous provider still routable")
	}
}

func TestCredentialsService_UpsertRejectsNameContainingSlash(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "my/provider", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true})
	if err == nil {
		t.Fatal("Upsert() with a '/' in the name error = nil, want a rejection")
	}
	if registry.ProviderCount() != 0 {
		t.Errorf("ProviderCount() = %d, want 0 (nothing should register for a rejected name)", registry.ProviderCount())
	}
}

func TestCredentialsService_UpsertRejectsUnresolvableCredential(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	// No API key and the "test" registration has no AllowAPIKeyless discovery
	// config, so this should fail to resolve.
	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name:    "no-key",
		Type:    "test",
		Enabled: true,
	})
	if err == nil {
		t.Fatal("Upsert() error = nil, want an error for a credential that cannot resolve")
	}
	if registry.ProviderCount() != 0 {
		t.Errorf("ProviderCount() = %d, want 0 (nothing should register on failure)", registry.ProviderCount())
	}
}

func TestCredentialsService_DeleteUnregistersProvider(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}
	if err := svc.Upsert(ctx, ManagedProviderCredential{Name: "gone", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if !registry.Supports("gone/test-model") {
		t.Fatal("provider did not register before delete")
	}

	if err := svc.Delete(ctx, "gone"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if registry.Supports("gone/test-model") {
		t.Error("Supports(gone/test-model) = true after Delete, want false")
	}
	if _, err := store.Get(ctx, "gone"); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("store.Get() after Delete error = %v, want ErrCredentialNotFound", err)
	}
}

func TestCredentialsService_DisablingUnregistersWithoutDeletingTheRow(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}
	cred := ManagedProviderCredential{Name: "pausable", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true}
	if err := svc.Upsert(ctx, cred); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	cred.Enabled = false
	if err := svc.Upsert(ctx, cred); err != nil {
		t.Fatalf("Upsert(disabled) error = %v", err)
	}

	if registry.Supports("pausable/test-model") {
		t.Error("Supports(pausable/test-model) = true after disabling, want false")
	}
	stored, err := store.Get(ctx, "pausable")
	if err != nil {
		t.Fatalf("store.Get() error = %v, want the row still present after disabling", err)
	}
	if stored.Enabled {
		t.Error("stored.Enabled = true, want false")
	}
}

func TestCredentialsService_DeclaredNamesAreManagedAndReadOnly(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, []string{"openai"}, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}

	if !svc.IsManaged("openai") {
		t.Error("IsManaged(openai) = false, want true (declared in config/env)")
	}
	if svc.IsManaged("not-declared") {
		t.Error("IsManaged(not-declared) = true, want false")
	}

	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "openai", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true})
	if err == nil {
		t.Fatal("Upsert() on a managed name error = nil, want a read-only rejection")
	}
	err = svc.Delete(ctx, "openai")
	if err == nil {
		t.Fatal("Delete() on a managed name error = nil, want a read-only rejection")
	}
}

func TestCredentialsService_ReloadSkipsShadowedStoreRowsAndAppliesTheRest(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	// A store row with the same name as a declared (config/env) provider must
	// be shadowed: it should never register, mirroring the mcpgateway store
	// precedence rule.
	if err := store.Upsert(ctx, ManagedProviderCredential{Name: "openai", Type: "test", APIKeys: []string{"sk-shadowed"}, Enabled: true}); err != nil {
		t.Fatalf("seed store.Upsert() error = %v", err)
	}
	if err := store.Upsert(ctx, ManagedProviderCredential{Name: "extra", Type: "test", APIKeys: []string{"sk-extra"}, Enabled: true}); err != nil {
		t.Fatalf("seed store.Upsert() error = %v", err)
	}
	if err := store.Upsert(ctx, ManagedProviderCredential{Name: "disabled", Type: "test", APIKeys: []string{"sk-disabled"}, Enabled: false}); err != nil {
		t.Fatalf("seed store.Upsert() error = %v", err)
	}

	svc, err := NewCredentialsService(ctx, factory, registry, store, []string{"openai"}, config.ResilienceConfig{})
	if err != nil {
		t.Fatalf("NewCredentialsService() error = %v", err)
	}
	_ = svc

	// Reload's model-catalog fetch is deliberately non-blocking (it mirrors
	// providers.Init's async startup so adding the credentials store never
	// turns gateway startup into a synchronous network sweep), so assert on
	// registration state, which register() sets synchronously before the
	// async fetch kicks off, rather than on Supports()/the fetched catalog.
	if registry.ProviderByName("openai") != nil {
		t.Error("provider registered for shadowed name 'openai'; declarative providers.Init should own it instead")
	}
	if registry.ProviderByName("extra") == nil {
		t.Error("ProviderByName(extra) = nil, want registered (non-shadowed enabled row should apply on Reload)")
	}
	if registry.ProviderByName("disabled") != nil {
		t.Error("ProviderByName(disabled) != nil, want nil (disabled row should not register)")
	}
}
