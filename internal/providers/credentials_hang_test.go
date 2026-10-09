package providers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
)

// startStuckSave saves a credential whose ${block:...} reference never
// resolves, the way opening a FIFO or reading a stalled mount does, and
// returns once the save is waiting on it. The resolver ignores its context.
func startStuckSave(t *testing.T, svc *CredentialsService, secrets *config.Secrets) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	require.NoError(t, secrets.Register("block", config.SecretResolverFunc(func(context.Context, string) (string, error) {
		once.Do(func() { close(entered) })
		<-release
		return "sk-late", nil
	})))
	t.Cleanup(func() { close(release) })

	go func() {
		_ = svc.Upsert(context.Background(), ManagedProviderCredential{Name: "stuck", Type: "test", APIKeys: []string{"${block:key}"}, Enabled: true})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the save never resolved its reference")
	}
}

// requireReturns fails the test unless fn returns within a few seconds.
func requireReturns(t *testing.T, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		require.NoError(t, err, what)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s waited on another save's secret resolution", what)
	}
}

func TestCredentialsService_SaveWaitingOnAReferenceDoesNotBlockOtherSaves(t *testing.T) {
	vault := &secretVault{values: map[string]string{"other": "sk-other"}}
	svc, secrets, _ := newSecretsTestService(t, newFakeCredentialStore(), vault)
	startStuckSave(t, svc, secrets)

	ctx := t.Context()
	requireReturns(t, "another save", func() error {
		return svc.Upsert(ctx, ManagedProviderCredential{Name: "other", Type: "test", APIKeys: []string{"${vault:other}"}, Enabled: true})
	})
	assert.NotNil(t, svc.registry.ProviderByName("other"))
	requireReturns(t, "a delete", func() error { return svc.Delete(ctx, "other") })
	assert.Nil(t, svc.registry.ProviderByName("other"))
}

func TestCredentialsService_SaveWaitingOnAReferenceDoesNotBlockRotation(t *testing.T) {
	vault := &secretVault{values: map[string]string{"key": "sk-1"}}
	svc, secrets, _ := newSecretsTestService(t, newFakeCredentialStore(), vault)
	ctx := t.Context()
	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "rotating", Type: "test", APIKeys: []string{"${vault:key}"}, Enabled: true}))
	startStuckSave(t, svc, secrets)

	vault.set("key", "sk-2")
	requireReturns(t, "rotation", func() error {
		return svc.RotateSecrets(ctx, []string{"provider_credentials.rotating.api_keys[0]"})
	})
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Equal(t, []string{"sk-2"}, svc.configs["rotating"].APIKeys)
}

// A save or delete that lands while rotation resolves wins: rotation does not
// install values resolved from the row it replaced.
func TestCredentialsService_RotationSkipsACredentialSavedMeanwhile(t *testing.T) {
	vault := &secretVault{values: map[string]string{"key": "sk-1", "new": "sk-new"}}
	store := newFakeCredentialStore()
	svc, secrets, _ := newSecretsTestService(t, store, vault)
	ctx := t.Context()
	// The second key's resolver replaces the stored row mid-rotation.
	hooked := false
	require.NoError(t, secrets.Register("hook", config.SecretResolverFunc(func(context.Context, string) (string, error) {
		if hooked {
			hooked = false
			assert.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "rotating", Type: "test", APIKeys: []string{"${vault:new}"}, Enabled: true}))
		}
		return "sk-hook", nil
	})))
	require.NoError(t, svc.Upsert(ctx, ManagedProviderCredential{Name: "rotating", Type: "test", APIKeys: []string{"${vault:key}", "${hook:x}"}, Enabled: true}))

	vault.set("key", "sk-2")
	hooked = true
	require.NoError(t, svc.RotateSecrets(ctx, []string{"provider_credentials.rotating.api_keys[0]"}))

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Equal(t, []string{"sk-1", "sk-hook"}, svc.configs["rotating"].APIKeys, "rotation must not install values resolved from a replaced row")
}
