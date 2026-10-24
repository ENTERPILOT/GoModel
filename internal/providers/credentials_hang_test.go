package providers

import (
	"context"
	"sync"
	"sync/atomic"
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

// gate is a ${gate:...} resolver that, once armed, blocks its next call
// until opened, so a test can interleave a save with a rotation.
type gate struct {
	armed   atomic.Bool
	entered chan struct{}
	opened  chan struct{}
	once    sync.Once
}

// open lets the blocked call return.
func (g *gate) open() { g.once.Do(func() { close(g.opened) }) }

func newGate(t *testing.T, secrets *config.Secrets) *gate {
	t.Helper()
	g := &gate{entered: make(chan struct{}), opened: make(chan struct{})}
	t.Cleanup(g.open)
	require.NoError(t, secrets.Register("gate", config.SecretResolverFunc(func(context.Context, string) (string, error) {
		if g.armed.CompareAndSwap(true, false) {
			close(g.entered)
			<-g.opened
		}
		return "sk-gate", nil
	})))
	return g
}

// waitEntered returns once the armed call is blocked.
func (g *gate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the gated resolution never started")
	}
}

// The save resolves the old key and then waits on another field while
// rotation installs the new key. Committing then must not restore the old
// key, which may already be revoked.
func TestCredentialsService_SaveDoesNotRestoreAKeyRotatedMeanwhile(t *testing.T) {
	vault := &secretVault{values: map[string]string{"key": "sk-1"}}
	svc, secrets, _ := newSecretsTestService(t, newFakeCredentialStore(), vault)
	g := newGate(t, secrets)
	ctx := t.Context()
	cred := ManagedProviderCredential{Name: "rotating", Type: "test", APIKeys: []string{"${vault:key}", "${gate:x}"}, Enabled: true}
	require.NoError(t, svc.Upsert(ctx, cred))

	g.armed.Store(true)
	saved := make(chan error, 1)
	go func() { saved <- svc.Upsert(ctx, cred) }()
	g.waitEntered(t)

	vault.set("key", "sk-2")
	require.NoError(t, svc.RotateSecrets(ctx, []string{"provider_credentials.rotating.api_keys[0]"}))
	g.open()
	require.NoError(t, <-saved)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Equal(t, []string{"sk-2", "sk-gate"}, svc.configs["rotating"].APIKeys)
}

// Rotation resolves and then waits while a save installs newer values.
// Rotation must not replace them with what it resolved before.
func TestCredentialsService_RotationDoesNotReplaceValuesSavedMeanwhile(t *testing.T) {
	vault := &secretVault{values: map[string]string{"key": "sk-1"}}
	svc, secrets, _ := newSecretsTestService(t, newFakeCredentialStore(), vault)
	g := newGate(t, secrets)
	ctx := t.Context()
	cred := ManagedProviderCredential{Name: "rotating", Type: "test", APIKeys: []string{"${vault:key}", "${gate:x}"}, Enabled: true}
	require.NoError(t, svc.Upsert(ctx, cred))

	vault.set("key", "sk-2")
	g.armed.Store(true)
	rotated := make(chan error, 1)
	go func() { rotated <- svc.RotateSecrets(ctx, []string{"provider_credentials.rotating.api_keys[0]"}) }()
	g.waitEntered(t)

	vault.set("key", "sk-3")
	require.NoError(t, svc.Upsert(ctx, cred))
	g.open()
	require.NoError(t, <-rotated)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Equal(t, []string{"sk-3", "sk-gate"}, svc.configs["rotating"].APIKeys)
}
