package providers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCredentialsService_BuildProviderPreservesManagedHookIdentity(t *testing.T) {
	var start llmclient.RequestInfo
	var end llmclient.ResponseInfo
	var firstChunk llmclient.ResponseInfo
	factory := NewProviderFactory()
	factory.SetHooks(llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			start = info
			return ctx
		},
		OnRequestEnd: func(_ context.Context, info llmclient.ResponseInfo) {
			end = info
		},
		OnStreamFirstChunk: func(_ context.Context, info llmclient.ResponseInfo) {
			firstChunk = info
		},
	})
	var providerHooks llmclient.Hooks
	factory.Add(Registration{
		Type: "test",
		New: func(_ ProviderConfig, opts ProviderOptions) core.Provider {
			providerHooks = opts.Hooks
			return &registryMockProvider{}
		},
	})

	service, err := NewCredentialsService(t.Context(), factory, NewModelRegistry(), newFakeCredentialStore(), nil, config.ResilienceConfig{})
	require.NoError(t, err)

	_, _, err = service.buildProvider(ManagedProviderCredential{
		Name:    "managed-eu",
		Type:    "test",
		APIKeys: []string{"sk-test"},
		Enabled: true,
	})
	require.NoError(t, err)

	providerHooks.OnRequestStart(t.Context(), llmclient.RequestInfo{})
	providerHooks.OnRequestEnd(t.Context(), llmclient.ResponseInfo{})
	providerHooks.OnStreamFirstChunk(t.Context(), llmclient.ResponseInfo{})
	require.Equal(t, "managed-eu", start.Provider)
	require.Equal(t, "test", start.ProviderType)
	require.Equal(t, "managed-eu", end.Provider)
	require.Equal(t, "test", end.ProviderType)
	require.Equal(t, "managed-eu", firstChunk.Provider)
	require.Equal(t, "test", firstChunk.ProviderType)
}

func TestCredentialsService_UpsertRegistersAndRoutesImmediately(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	require.NoError(t, err)

	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name:    "my-openai",
		Type:    "test",
		APIKeys: []string{"sk-test"},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.True(t, registry.Supports("my-openai/test-model"))

	stored, err := store.Get(ctx, "my-openai")
	require.NoError(t, err)
	assert.Equal(t, "test", stored.Type)
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
	require.NoError(t, err)

	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name:    "bad-key",
		Type:    "test",
		APIKeys: []string{"sk-wrong"},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.NotNil(t, registry.ProviderByName("bad-key"))
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
	require.NoError(t, err)
	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "flaky", Type: "test", APIKeys: []string{"sk-good"}, Enabled: true})
	require.NoError(t, err)
	require.True(t, registry.Supports("flaky/test-model"))

	// An edit that strips the only API key: the "test" registration doesn't
	// allow keyless credentials, so this must fail to resolve.
	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "flaky", Type: "test", Enabled: true})
	require.Error(t, err)
	assert.NotNil(t, registry.ProviderByName("flaky"))
	assert.True(t, registry.Supports("flaky/test-model"))
}

func TestCredentialsService_UpsertRejectsNameContainingSlash(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	require.NoError(t, err)

	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "my/provider", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true})
	require.Error(t, err)
	assert.Equal(t, 0, registry.ProviderCount())
}

func TestCredentialsService_UpsertRejectsUnresolvableCredential(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	require.NoError(t, err)

	// No API key and the "test" registration has no AllowAPIKeyless discovery
	// config, so this should fail to resolve.
	err = svc.Upsert(ctx, ManagedProviderCredential{
		Name:    "no-key",
		Type:    "test",
		Enabled: true,
	})
	require.Error(t, err)
	assert.Equal(t, 0, registry.ProviderCount())
}

func TestCredentialsService_DeleteUnregistersProvider(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	require.NoError(t, err)
	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "gone", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true})
	require.NoError(t, err)
	require.True(t, registry.Supports("gone/test-model"))
	err = svc.Delete(ctx, "gone")
	require.NoError(t, err)
	assert.False(t, registry.Supports("gone/test-model"))
	_, err = store.Get(ctx, "gone")
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}

func TestCredentialsService_DisablingUnregistersWithoutDeletingTheRow(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, nil, config.ResilienceConfig{})
	require.NoError(t, err)

	cred := ManagedProviderCredential{Name: "pausable", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true}
	err = svc.Upsert(ctx, cred)
	require.NoError(t, err)

	cred.Enabled = false
	err = svc.Upsert(ctx, cred)
	require.NoError(t, err)
	assert.False(t, registry.Supports("pausable/test-model"))

	stored, err := store.Get(ctx, "pausable")
	require.NoError(t, err)
	assert.False(t, stored.Enabled)
}

func TestCredentialsService_DeclaredNamesAreManagedAndReadOnly(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()

	svc, err := NewCredentialsService(ctx, factory, registry, store, []string{"openai"}, config.ResilienceConfig{})
	require.NoError(t, err)
	assert.True(t, svc.IsManaged("openai"))
	assert.False(t, svc.IsManaged("not-declared"))

	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "openai", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true})
	require.Error(t, err)

	err = svc.Delete(ctx, "openai")
	require.Error(t, err)
}

func TestCredentialsService_ReloadSkipsShadowedStoreRowsAndAppliesTheRest(t *testing.T) {
	ctx := t.Context()
	factory := newCredentialsTestFactory(t)
	registry := NewModelRegistry()
	store := newFakeCredentialStore()
	err := // A store row with the same name as a declared (config/env) provider must
		// be shadowed: it should never register, mirroring the mcpgateway store
		// precedence rule.
		store.Upsert(ctx, ManagedProviderCredential{Name: "openai", Type: "test", APIKeys: []string{"sk-shadowed"}, Enabled: true})
	require.NoError(t, err)
	err = store.Upsert(ctx, ManagedProviderCredential{Name: "extra", Type: "test", APIKeys: []string{"sk-extra"}, Enabled: true})
	require.NoError(t, err)
	err = store.Upsert(ctx, ManagedProviderCredential{Name: "disabled", Type: "test", APIKeys: []string{"sk-disabled"}, Enabled: false})
	require.NoError(t, err)

	svc, err := NewCredentialsService(ctx, factory, registry, store, []string{"openai"}, config.ResilienceConfig{})
	require.NoError(t, err)

	_ = svc

	// Reload's model-catalog fetch is deliberately non-blocking (it mirrors
	// providers.Init's async startup so adding the credentials store never
	// turns gateway startup into a synchronous network sweep), so assert on
	// registration state, which register() sets synchronously before the
	// async fetch kicks off, rather than on Supports()/the fetched catalog.
	assert.Nil(t, registry.ProviderByName("openai"))
	assert.NotNil(t, registry.ProviderByName("extra"))
	assert.Nil(t, registry.ProviderByName("disabled"))
}

// TestCredentialsService_ConfiguredProvidersCarryGlobalResilience guards the
// admin status endpoint's view of dashboard-registered providers: the config
// the service exposes must carry the same merged resilience settings
// resolveProviders produces for config.yaml/env providers, and must track
// the credential's lifecycle (disable, delete).
func TestCredentialsService_ConfiguredProvidersCarryGlobalResilience(t *testing.T) {
	ctx := t.Context()
	global := config.ResilienceConfig{
		Retry: config.RetryConfig{
			MaxRetries:     3,
			InitialBackoff: time.Second,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2,
			JitterFactor:   0.1,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 5,
			SuccessThreshold: 2,
			Timeout:          30 * time.Second,
		},
	}
	want := SanitizeProviderConfigs(map[string]ProviderConfig{
		"my-openai": {Type: "test", Resilience: global},
	})[0]

	svc, err := NewCredentialsService(ctx, newCredentialsTestFactory(t), NewModelRegistry(), newFakeCredentialStore(), nil, global)
	require.NoError(t, err)
	got := svc.ConfiguredProviders()
	require.Empty(t, got)

	cred := ManagedProviderCredential{Name: "my-openai", Type: "test", APIKeys: []string{"sk-test"}, Enabled: true}
	err = svc.Upsert(ctx, cred)
	require.NoError(t, err)

	got = svc.ConfiguredProviders()
	require.Len(t, got, 1)
	assert.Equal(t, want.Name, got[0].Name)
	assert.Equal(t, want.Type, got[0].Type)
	assert.Equal(t, want.Resilience, got[0].Resilience)

	tests := []struct {
		name string
		act  func() error
	}{
		{name: "disabled", act: func() error {
			cred.Enabled = false
			return svc.Upsert(ctx, cred)
		}},
		{name: "deleted", act: func() error {
			cred.Enabled = true
			if err := svc.Upsert(ctx, cred); err != nil {
				return err
			}
			return svc.Delete(ctx, cred.Name)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.act()
			require.NoError(t, err)
			got := svc.ConfiguredProviders()
			assert.Empty(t, got, "ConfiguredProviders() after %s = %#v, want empty", tt.name, got)
		})
	}
}
