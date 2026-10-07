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

// A resolver that ignores cancellation must not hold up shutdown, and the
// check it strands must neither act nor swallow the notification: the next
// serving generation has to see it.
func TestSecretRotationCloseDoesNotWaitForAStuckResolver(t *testing.T) {
	previous := secretRotationCloseTimeout
	secretRotationCloseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { secretRotationCloseTimeout = previous })

	notifier := config.NewSecretNotifier()
	entered := make(chan struct{})
	release := make(chan struct{})
	secrets := config.NewSecrets()
	secrets.SetNotifier(notifier)
	var blocking atomic.Bool
	require.NoError(t, secrets.Register("vault", config.SecretResolverFunc(func(context.Context, string) (string, error) {
		if blocking.Load() {
			close(entered)
			<-release // ignores its context
			return "d2", nil
		}
		return "d1", nil
	})))
	fields := map[string]string{"storage.postgresql.url": "${vault:dsn}"}
	require.NoError(t, secrets.ResolveFields(t.Context(), "", &fields))

	reloads := make(chan string, 1)
	rotation := &secretRotation{
		secrets:  secrets,
		planKeys: func(*config.SecretRecheck) keySwap { return nil },
		reload:   func(reason string) { reloads <- reason },
	}
	rotation.start(t.Context())
	blocking.Store(true)
	secrets.NotifyChanged()
	<-entered

	closed := make(chan struct{})
	go func() {
		assert.NoError(t, rotation.Close())
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a resolver that ignores cancellation")
	}

	close(release)
	select {
	case <-notifier.C():
	case <-time.After(5 * time.Second):
		t.Fatal("the stranded check did not hand its notification on")
	}
	assert.Empty(t, reloads, "a check outliving its generation must not act")
}

func (w *secretRotation) handoffState() (stranded, held bool) {
	w.handoff.Lock()
	defer w.handoff.Unlock()
	return w.stranded, w.held
}

// hangingRotation is a serving watcher whose resolver, once hang is set,
// blocks its first call until release, ignoring its context.
type hangingRotation struct {
	rotation *secretRotation
	secrets  *config.Secrets
	notifier *config.SecretNotifier
	calls    atomic.Int32
	hang     atomic.Bool
	entered  chan struct{}
	release  func()
	reloads  chan string
}

func newHangingRotation(t *testing.T) *hangingRotation {
	t.Helper()
	previous := secretRecheckTimeout
	secretRecheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { secretRecheckTimeout = previous })

	h := &hangingRotation{
		secrets:  config.NewSecrets(),
		notifier: config.NewSecretNotifier(),
		entered:  make(chan struct{}),
		reloads:  make(chan string, 4),
	}
	released := make(chan struct{})
	var once sync.Once
	h.release = func() { once.Do(func() { close(released) }) }
	// Registered first, so it runs last: a failing test never leaves the
	// resolver blocked.
	t.Cleanup(h.release)

	h.secrets.SetNotifier(h.notifier)
	require.NoError(t, h.secrets.Register("vault", config.SecretResolverFunc(func(context.Context, string) (string, error) {
		if !h.hang.Load() {
			return "d1", nil
		}
		if h.calls.Add(1) == 1 {
			close(h.entered)
			<-released // ignores its context
		}
		return "d2", nil
	})))
	fields := map[string]string{"storage.postgresql.url": "${vault:dsn}"}
	require.NoError(t, h.secrets.ResolveFields(t.Context(), "", &fields))

	h.rotation = &secretRotation{
		secrets:  h.secrets,
		planKeys: func(*config.SecretRecheck) keySwap { return nil },
		reload:   func(reason string) { h.reloads <- reason },
	}
	h.rotation.start(t.Context())
	t.Cleanup(func() { assert.NoError(t, h.rotation.Close()) })
	return h
}

// strandAndHold hangs the resolver, lets the check give up on it, and sends
// a second notification that the watcher holds.
func (h *hangingRotation) strandAndHold(t *testing.T) {
	t.Helper()
	h.hang.Store(true)
	h.secrets.NotifyChanged()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not start a re-check")
	}
	require.Eventually(t, func() bool { stranded, _ := h.rotation.handoffState(); return stranded },
		5*time.Second, 5*time.Millisecond, "the check did not give up on the hung resolver")

	// The watcher is free again: this notice is held, not stacked on the
	// resolver that is still hung.
	h.secrets.NotifyChanged()
	require.Eventually(t, func() bool { _, held := h.rotation.handoffState(); return held },
		5*time.Second, 5*time.Millisecond, "the second notification was not held")
	assert.Equal(t, int32(1), h.calls.Load(), "no second re-check while the first is stranded")
	assert.Empty(t, h.reloads, "a timed-out re-check must not act")
}

// A resolver that ignores its context must not wedge the serving watcher:
// the check gives up after secretRecheckTimeout, later notifications are held
// without starting another re-check the resolver would block, and the change
// is applied once the resolver returns.
func TestSecretRotationRecheckTimeoutDoesNotWedgeTheWatcher(t *testing.T) {
	h := newHangingRotation(t)
	h.strandAndHold(t)

	h.release()
	select {
	case reason := <-h.reloads:
		assert.Contains(t, reason, "storage.postgresql.url")
	case <-time.After(5 * time.Second):
		t.Fatal("the held notification was not checked once the resolver returned")
	}
}

// A notification held for a stranded re-check is forwarded when the watcher
// stops, without waiting for the resolver: the generation serving next must
// re-check, or it keeps the old credential with nothing pending.
func TestSecretRotationForwardsAHeldNoticeWhenItStops(t *testing.T) {
	h := newHangingRotation(t)
	h.strandAndHold(t)

	require.NoError(t, h.rotation.Close())
	select {
	case <-h.notifier.C():
	case <-time.After(5 * time.Second):
		t.Fatal("the held notification died with the watcher")
	}
	_, held := h.rotation.handoffState()
	assert.False(t, held, "the notice is forwarded once, not again when the resolver returns")
	assert.Empty(t, h.reloads, "the stopped watcher must not act")
}
