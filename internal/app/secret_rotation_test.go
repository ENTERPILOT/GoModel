package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rotationTestVault struct {
	mu     sync.Mutex
	values map[string]string
	err    error
}

func (v *rotationTestVault) set(reference, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[reference] = value
}

func (v *rotationTestVault) fail(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

func (v *rotationTestVault) ResolveSecret(_ context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return "", v.err
	}
	return v.values[reference], nil
}

type fakeKeySwap struct {
	providers []string
	applied   atomic.Int32
}

func (s *fakeKeySwap) Providers() []string { return s.providers }
func (s *fakeKeySwap) Apply()              { s.applied.Add(1) }

type rotationHarness struct {
	vault    *rotationTestVault
	rotation *secretRotation
	swap     *fakeKeySwap // returned by the planner; nil means "needs reload"
	planned  atomic.Int32
	reloads  chan string
}

func newRotationHarness(t *testing.T, swap *fakeKeySwap) *rotationHarness {
	t.Helper()
	h := &rotationHarness{
		vault:   &rotationTestVault{values: map[string]string{"openai": "k1", "dsn": "d1"}},
		swap:    swap,
		reloads: make(chan string, 10),
	}
	secrets := config.NewSecrets()
	require.NoError(t, secrets.Register("vault", h.vault))
	// Map keys become the recorded field paths.
	fields := map[string]string{"providers.openai.api_key": "${vault:openai}", "storage.postgresql.url": "${vault:dsn}"}
	require.NoError(t, secrets.ResolveFields(t.Context(), "", &fields))
	h.rotation = &secretRotation{
		secrets: secrets,
		planKeys: func(*config.SecretRecheck) keySwap {
			h.planned.Add(1)
			if h.swap == nil {
				return nil
			}
			return h.swap
		},
		pinned: []string{"embedder"},
		reload: func(reason string) { h.reloads <- reason },
	}
	return h
}

func TestSecretRotationSwapsKeysInPlace(t *testing.T) {
	h := newRotationHarness(t, &fakeKeySwap{providers: []string{"openai"}})
	h.vault.set("openai", "k2")

	h.rotation.check(t.Context())
	assert.Equal(t, int32(1), h.swap.applied.Load())
	assert.Empty(t, h.reloads)

	// The swap was committed, so the same values are not applied twice.
	h.rotation.check(t.Context())
	assert.Equal(t, int32(1), h.planned.Load())
	assert.Equal(t, int32(1), h.swap.applied.Load())
}

func TestSecretRotationReloadsForOtherFields(t *testing.T) {
	h := newRotationHarness(t, nil)
	h.vault.set("dsn", "d2")

	h.rotation.check(t.Context())
	require.Len(t, h.reloads, 1)
	reason := <-h.reloads
	assert.Contains(t, reason, "storage.postgresql.url")
	assert.NotContains(t, reason, "d2")

	// Not committed: a reload that fails is retried on the next notification.
	h.rotation.check(t.Context())
	assert.Len(t, h.reloads, 1)
}

func TestSecretRotationReloadsWhenPinnedProviderRotates(t *testing.T) {
	h := newRotationHarness(t, &fakeKeySwap{providers: []string{"embedder"}})
	h.vault.set("openai", "k2")

	h.rotation.check(t.Context())
	assert.Zero(t, h.swap.applied.Load())
	assert.Len(t, h.reloads, 1)
}

func TestSecretRotationKeepsValuesWhenResolutionFails(t *testing.T) {
	h := newRotationHarness(t, &fakeKeySwap{providers: []string{"openai"}})
	h.vault.set("openai", "k2")
	h.vault.fail(errors.New("backend unavailable"))

	h.rotation.check(t.Context())
	assert.Zero(t, h.planned.Load())
	assert.Zero(t, h.swap.applied.Load())
	assert.Empty(t, h.reloads)

	// Once the backend recovers, the pending change applies.
	h.vault.fail(nil)
	h.rotation.check(t.Context())
	assert.Equal(t, int32(1), h.swap.applied.Load())
}

func TestSecretRotationWithoutReloadHook(t *testing.T) {
	h := newRotationHarness(t, nil)
	h.rotation.reload = nil
	h.vault.set("dsn", "d2")
	assert.NotPanics(t, func() { h.rotation.check(t.Context()) })
}

func TestSecretRotationWatchesNotifications(t *testing.T) {
	h := newRotationHarness(t, nil)
	h.rotation.start(t.Context())
	t.Cleanup(func() { assert.NoError(t, h.rotation.Close()) })

	h.vault.set("dsn", "d2")
	for range 50 {
		h.rotation.secrets.NotifyChanged()
	}
	select {
	case <-h.reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("NotifyChanged did not trigger a re-check")
	}

	// Close waits for a check in progress, so the count is final here: the
	// fifty notifications coalesced into at most two checks (the one running
	// and one pending).
	require.NoError(t, h.rotation.Close())
	require.NoError(t, h.rotation.Close(), "Close is idempotent")
	assert.LessOrEqual(t, h.planned.Load(), int32(2))
}

// Generations share one notifier, so only the serving generation may listen:
// a built but not yet serving (or abandoned) one must leave it alone.
func TestSecretRotationListensOnlyOnceServing(t *testing.T) {
	notifier := config.NewSecretNotifier()
	serving := newRotationHarness(t, nil)
	pending := newRotationHarness(t, nil)
	serving.rotation.secrets.SetNotifier(notifier)
	pending.rotation.secrets.SetNotifier(notifier)
	serving.vault.set("dsn", "d2")
	pending.vault.set("dsn", "d2")

	serving.rotation.start(t.Context())
	t.Cleanup(func() { assert.NoError(t, serving.rotation.Close()) })
	pending.rotation.secrets.NotifyChanged()

	select {
	case <-serving.reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("the serving generation did not receive a notification sent through another generation")
	}
	require.NoError(t, pending.rotation.Close())
	assert.Zero(t, pending.planned.Load())

	pending.rotation.start(t.Context())
	assert.Nil(t, pending.rotation.cancel, "a closed watcher does not start")
}
