package providers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// secretVault is a mutable ${vault:...} backend that remembers the field each
// lookup was made for.
type secretVault struct {
	mu     sync.Mutex
	values map[string]string
	fields []string
}

func (v *secretVault) set(reference, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[reference] = value
}

func (v *secretVault) ResolveSecret(ctx context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	field, _ := config.SecretFieldFromContext(ctx)
	v.fields = append(v.fields, field)
	value, ok := v.values[reference]
	if !ok {
		return "", errors.New("secret " + reference + " not found")
	}
	return value, nil
}

// secretWriterFake stores secrets under ${vault:written/<n>} references.
type secretWriterFake struct {
	vault   *secretVault
	keys    []config.SecretKey
	deleted []string
}

func (w *secretWriterFake) WriteSecret(_ context.Context, key config.SecretKey, value string) (string, error) {
	w.keys = append(w.keys, key)
	reference := "written/" + key.ID + "/" + key.Field
	w.vault.set(reference, value)
	return "${vault:" + reference + "}", nil
}

func (w *secretWriterFake) DeleteSecret(_ context.Context, reference string) error {
	w.deleted = append(w.deleted, reference)
	return nil
}

func (w *secretWriterFake) OwnsReference(reference string) bool {
	return strings.HasPrefix(reference, "${vault:written/")
}

// builtProviders records every provider the factory constructs.
type builtProviders struct {
	mu      sync.Mutex
	configs []ProviderConfig
	keys    []*Keyring
}

func (b *builtProviders) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.configs)
}

func (b *builtProviders) last() (ProviderConfig, *Keyring) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.configs[len(b.configs)-1], b.keys[len(b.keys)-1]
}

func newSecretsTestService(t *testing.T, store *fakeCredentialStore, vault *secretVault) (*CredentialsService, *config.Secrets, *builtProviders) {
	t.Helper()
	built := &builtProviders{}
	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			built.mu.Lock()
			built.configs = append(built.configs, cfg)
			built.keys = append(built.keys, opts.Keys)
			built.mu.Unlock()
			return &registryMockProvider{name: cfg.Type, modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "test-model", Object: "model", OwnedBy: "test"}},
			}}
		},
	})
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", vault))
	svc, err := NewCredentialsService(t.Context(), factory, NewModelRegistry(), store, nil, config.ResilienceConfig{}, secrets)
	require.NoError(t, err)
	return svc, secrets, built
}

func TestCredentialsService_ResolvesReferencesWhenBuilding(t *testing.T) {
	vault := &secretVault{values: map[string]string{"openai": "sk-resolved"}}
	store := newFakeCredentialStore()
	svc, _, built := newSecretsTestService(t, store, vault)

	err := svc.Upsert(t.Context(), ManagedProviderCredential{
		Name:    "my-openai",
		Type:    "test",
		APIKeys: []string{"${vault:openai}"},
		Enabled: true,
	})
	require.NoError(t, err)

	stored, err := store.Get(t.Context(), "my-openai")
	require.NoError(t, err)
	assert.Equal(t, []string{"${vault:openai}"}, stored.APIKeys, "the reference is stored, not the secret")
	cfg, keys := built.last()
	assert.Equal(t, []string{"sk-resolved"}, cfg.APIKeys)
	assert.Equal(t, "sk-resolved", keys.Primary())
	assert.Contains(t, vault.fields, "provider_credentials.my-openai.api_keys[0]")
}

func TestCredentialsService_UnresolvableReferenceIsAFieldError(t *testing.T) {
	vault := &secretVault{values: map[string]string{}}
	store := newFakeCredentialStore()
	svc, _, _ := newSecretsTestService(t, store, vault)

	for _, cred := range []ManagedProviderCredential{
		{Name: "a", Type: "test", APIKeys: []string{"sk-ok", "${nope:key}"}, Enabled: true},
		{Name: "b", Type: "test", APIKeys: []string{"${vault:missing}"}, Enabled: false},
		{Name: "c", Type: "test", APIKeys: []string{"sk-ok"}, ProxyURL: "http://u:${vault:missing}@proxy:3128", Enabled: true},
	} {
		err := svc.Upsert(t.Context(), cred)
		fieldErr, ok := errors.AsType[*CredentialFieldError](err)
		require.True(t, ok, "%s: %v", cred.Name, err)
		_, stored := store.rows[cred.Name]
		assert.False(t, stored, "%s must not be persisted", cred.Name)
		switch cred.Name {
		case "a":
			assert.Equal(t, CredentialFieldAPIKeys, fieldErr.Field)
			assert.Contains(t, fieldErr.Message, "provider_credentials.a.api_keys[1]")
			require.ErrorIs(t, err, config.ErrUnknownSecretScheme)
		case "b":
			assert.Equal(t, CredentialFieldAPIKeys, fieldErr.Field)
		case "c":
			assert.Equal(t, CredentialFieldProxyURL, fieldErr.Field)
		}
		assert.NotContains(t, fieldErr.Message, "sk-ok")
	}
}

func TestCredentialsService_ReloadSkipsRowsWhoseReferencesFail(t *testing.T) {
	vault := &secretVault{values: map[string]string{"good": "sk-good"}}
	store := newFakeCredentialStore()
	store.rows["good"] = ManagedProviderCredential{Name: "good", Type: "test", APIKeys: []string{"${vault:good}"}, Enabled: true}
	store.rows["broken"] = ManagedProviderCredential{Name: "broken", Type: "test", APIKeys: []string{"${vault:gone}"}, Enabled: true}

	svc, _, _ := newSecretsTestService(t, store, vault)

	assert.NotNil(t, svc.registry.ProviderByName("good"))
	assert.Nil(t, svc.registry.ProviderByName("broken"))
}

func TestCredentialsService_RotateSecretsSwapsKeysOrRebuilds(t *testing.T) {
	vault := &secretVault{values: map[string]string{"key": "sk-1", "proxy": "p1", "other": "sk-other"}}
	store := newFakeCredentialStore()
	svc, secrets, built := newSecretsTestService(t, store, vault)
	ctx := t.Context()

	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "rotating", Type: "test", APIKeys: []string{"${vault:key}"}, ProxyURL: "http://u:${vault:proxy}@proxy:3128", Enabled: true}))
	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "untouched", Type: "test", APIKeys: []string{"${vault:other}"}, Enabled: true}))
	_, keys := built.last()
	untouchedBuilds := built.count()
	builtRotating := svc.registry.ProviderByName("rotating")

	// Only the API key changed: swapped into the live keyring.
	vault.set("key", "sk-2")
	recheck, err := secrets.Recheck(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"provider_credentials.rotating.api_keys[0]"}, recheck.Fields())
	require.NoError(t, svc.RotateSecrets(ctx, recheck.Fields()))
	assert.Equal(t, untouchedBuilds, built.count(), "a key-only change does not rebuild")
	assert.Same(t, builtRotating, svc.registry.ProviderByName("rotating"))
	svc.mu.RLock()
	assert.Equal(t, "sk-2", svc.keyrings["rotating"].Primary())
	assert.Equal(t, "sk-other", svc.keyrings["untouched"].Primary())
	svc.mu.RUnlock()
	assert.Equal(t, "sk-other", keys.Primary())

	recheck, err = secrets.Recheck(ctx)
	require.NoError(t, err)
	assert.Empty(t, recheck.Fields(), "the swap is recorded")

	// The proxy password changed: that provider alone is rebuilt.
	vault.set("proxy", "p2")
	recheck, err = secrets.Recheck(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.RotateSecrets(ctx, recheck.Fields()))
	assert.Equal(t, untouchedBuilds+1, built.count())
	cfg, _ := built.last()
	assert.Equal(t, "rotating", cfg.Name)
	assert.Equal(t, "http://u:p2@proxy:3128", cfg.ProxyURL)

	// A failed lookup keeps the provider as it is.
	delete(vault.values, "key")
	err = svc.RotateSecrets(ctx, []string{"provider_credentials.rotating.api_keys[0]"})
	require.Error(t, err)
	assert.NotNil(t, svc.registry.ProviderByName("rotating"))
	assert.Equal(t, untouchedBuilds+1, built.count())
}

func TestCredentialsService_SecretWriter(t *testing.T) {
	vault := &secretVault{values: map[string]string{"hand": "sk-hand"}}
	store := newFakeCredentialStore()
	svc, secrets, built := newSecretsTestService(t, store, vault)
	writer := &secretWriterFake{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()

	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"sk-typed", "${vault:hand}"}, Enabled: true}))
	stored := store.rows["w"]
	assert.Equal(t, []string{"${vault:written/w/api_keys[0]}", "${vault:hand}"}, stored.APIKeys)
	assert.Equal(t, []config.SecretKey{{Entity: CredentialSecretEntity, ID: "w", Field: "api_keys[0]"}}, writer.keys)
	cfg, _ := built.last()
	assert.Equal(t, []string{"sk-typed", "sk-hand"}, cfg.APIKeys)

	// Replacing the written key releases it once the new row is stored; the
	// hand-written reference is never deleted.
	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"${vault:hand}"}, Enabled: true}))
	assert.Equal(t, []string{"${vault:written/w/api_keys[0]}"}, writer.deleted)

	// A save that fails after the write releases what it wrote.
	writer.deleted = nil
	err := svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"sk-new", "${vault:missing}"}, Enabled: true})
	require.Error(t, err)
	assert.Equal(t, []string{"${vault:written/w/api_keys[0]}"}, writer.deleted)
	assert.Equal(t, []string{"${vault:hand}"}, store.rows["w"].APIKeys)

	// Deleting the credential releases the written secrets it holds.
	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"sk-again"}, Enabled: true}))
	writer.deleted = nil
	require.NoError(t, svc.Delete(ctx, "w"))
	assert.Equal(t, []string{"${vault:written/w/api_keys[0]}"}, writer.deleted)
}

// A writer may hand back the reference the stored row already holds. A save
// that fails afterwards must not delete it: the stored row still uses it.
func TestCredentialsService_FailedSaveKeepsReferencesTheStoredRowHolds(t *testing.T) {
	vault := &secretVault{values: map[string]string{}}
	store := newFakeCredentialStore()
	svc, secrets, _ := newSecretsTestService(t, store, vault)
	writer := &secretWriterFake{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()

	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"sk-1"}, Enabled: true}))
	require.Equal(t, []string{"${vault:written/w/api_keys[0]}"}, store.rows["w"].APIKeys)

	err := svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"sk-2", "${vault:missing}"}, Enabled: true})
	require.Error(t, err)
	assert.Empty(t, writer.deleted)
	assert.Equal(t, []string{"${vault:written/w/api_keys[0]}"}, store.rows["w"].APIKeys)
}

// A writer-owned reference is only saved to the credential whose stored row
// holds it: a stale form of a deleted credential, or a reference copied from
// another one, may name a secret that was already released.
func TestCredentialsService_RejectsWriterReferencesTheRowDoesNotHold(t *testing.T) {
	vault := &secretVault{values: map[string]string{}}
	store := newFakeCredentialStore()
	svc, secrets, _ := newSecretsTestService(t, store, vault)
	writer := &secretWriterFake{vault: vault}
	secrets.SetWriter(writer)
	ctx := t.Context()
	owned := "${vault:written/w/api_keys[0]}"

	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{"sk-typed"}, Enabled: true}))
	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{owned, "sk-more"}, Enabled: true}), "the stored row holds it")

	err := svc.Upsert(ctx, ManagedProviderCredential{Name: "copy", Type: "test", APIKeys: []string{owned}, Enabled: true})
	require.ErrorIs(t, err, config.ErrSecretNotHeld)
	fieldErr, ok := errors.AsType[*CredentialFieldError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, CredentialFieldAPIKeys, fieldErr.Field)
	assert.NotContains(t, store.rows, "copy")

	require.NoError(t, svc.Delete(ctx, "w"))
	writer.deleted = nil
	err = svc.Upsert(ctx, ManagedProviderCredential{Name: "w", Type: "test", APIKeys: []string{owned}, Enabled: true})
	require.ErrorIs(t, err, config.ErrSecretNotHeld)
	assert.NotContains(t, store.rows, "w")
	assert.Empty(t, writer.deleted)
}
