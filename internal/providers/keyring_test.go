package providers

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKeyring(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{name: "no keys", keys: nil},
		{name: "only empty keys", keys: []string{"", ""}},
		{name: "single key", keys: []string{"k1"}, want: []string{"k1"}},
		{name: "preserves order", keys: []string{"k1", "k2", "k3"}, want: []string{"k1", "k2", "k3"}},
		{name: "drops empty keys", keys: []string{"k1", "", "k2"}, want: []string{"k1", "k2"}},
		{name: "drops duplicates", keys: []string{"k1", "k2", "k1"}, want: []string{"k1", "k2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring := NewKeyring(tt.keys...)

			if len(tt.want) == 0 {
				require.Nil(t, ring)

				return
			}
			require.Equal(t, len(tt.want), ring.Len())

			// One full cycle reproduces the configured order.
			for i, want := range tt.want {
				assert.Equal(t, want, ring.Next(), "Next() call %d", i)
			}
		})
	}
}

func TestKeyringPinsIdentifiedSessionsByDefault(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")
	want := ring.NextForSession("conversation-42")
	for range 10 {
		require.Equal(t, want, ring.NextForSession("conversation-42"))
	}

	ctx := core.WithSessionID(context.Background(), "conversation-42")
	got := ring.NextForContext(ctx)
	require.Equal(t, want, got)
}

func TestKeyringSessionStickinessCanBeDisabled(t *testing.T) {
	ring := NewKeyringWithSessionStickiness(false, "k1", "k2")
	got := []string{
		ring.NextForSession("same-session"),
		ring.NextForSession("same-session"),
		ring.NextForSession("same-session"),
	}
	want := []string{"k1", "k2", "k1"}
	require.True(t, equalStrings(got, want), "disabled sticky keys = %v, want round robin %v", got, want)
}

func TestKeyringSessionlessTrafficRemainsRoundRobin(t *testing.T) {
	ring := NewKeyring("k1", "k2")
	got := []string{ring.NextForContext(context.Background()), ring.NextForContext(context.Background())}
	want := []string{"k1", "k2"}
	require.True(t, equalStrings(got, want), "sessionless keys = %v, want %v", got, want)
}

func TestKeyringStableForContext(t *testing.T) {
	sticky := NewKeyring("k1", "k2")
	ctx := core.WithSessionID(context.Background(), "conversation-42")
	want := sticky.NextForContext(ctx)
	for range 3 {
		got, ok := sticky.StableForContext(ctx)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
	got := sticky.Next()
	require.Equal(t, "k1", got)

	for name, ring := range map[string]*Keyring{
		"sessionless": sticky,
		"disabled":    NewKeyringWithSessionStickiness(false, "k1", "k2"),
	} {
		t.Run(name, func(t *testing.T) {
			testCtx := context.Background()
			if name == "disabled" {
				testCtx = ctx
			}
			key, ok := ring.StableForContext(testCtx)
			require.False(t, ok)
			require.Empty(t, key)
		})
	}
}

func TestKeyringRendezvousHashingOnlyRemapsSessionsOnChangedKey(t *testing.T) {
	before := NewKeyring("k1", "k2", "k3")
	after := NewKeyring("k1", "k3")
	for i := range 1000 {
		session := fmt.Sprintf("session-%d", i)
		previous := before.NextForSession(session)
		current := after.NextForSession(session)
		if previous != "k2" {
			require.Equal(t, previous, current, "%q moved although its key remains", session)
		}
	}
}

func TestKeyringNextCyclesRoundRobin(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")

	// Two full cycles: the ring must wrap, not run dry.
	want := []string{"k1", "k2", "k3", "k1", "k2", "k3"}
	for i, expected := range want {
		got := ring.Next()
		assert.Equal(t, expected, got, "Next() call %d", i+1)
	}
}

// A single key must behave exactly as it did before rotation existed: every
// request presents the same credential, so provider prompt caching still works.
func TestKeyringSingleKeyNeverRotates(t *testing.T) {
	ring := NewKeyring("only")

	assert.False(t, ring.Rotates())

	for i := range 3 {
		got := ring.Next()
		assert.Equal(t, "only", got, "Next() call %d", i+1)
	}
}

// Keyless providers (Ollama, vLLM) and constructors invoked outside the factory
// hold a nil ring; every method must stay safe.
func TestKeyringNilIsEmpty(t *testing.T) {
	var ring *Keyring
	got := ring.Next()
	assert.Empty(t, got)
	got = ring.Primary()
	assert.Empty(t, got)
	assert.Zero(t, ring.Len())
	assert.False(t, ring.Rotates())
}

func TestKeyringPrimaryDoesNotAdvance(t *testing.T) {
	ring := NewKeyring("k1", "k2")

	for range 3 {
		got := ring.Primary()
		require.Equal(t, "k1", got)
	}
	got := ring.Next()
	assert.Equal(t, "k1", got)
}

// Providers are shared across concurrent requests, so the rotation must both be
// race-free and hand out each key an equal number of times.
func TestKeyringNextIsConcurrentAndEven(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")

	const perKey = 200
	total := perKey * ring.Len()

	var mu sync.Mutex
	counts := make(map[string]int, ring.Len())

	var wg sync.WaitGroup
	for range total {
		wg.Go(func() {
			key := ring.Next()
			mu.Lock()
			counts[key]++
			mu.Unlock()
		})
	}
	wg.Wait()

	for _, key := range []string{"k1", "k2", "k3"} {
		assert.Equal(t, perKey, counts[key], "key %q usage", key)
	}
}

func TestProviderOptionsKeyringFallsBackToStaticKey(t *testing.T) {
	// Constructed outside the factory: no ring supplied, so the single
	// constructor key is used.
	opts := ProviderOptions{}
	got := opts.Keyring("sk-static").Next()
	assert.Equal(t, "sk-static", got)

	// Built by the factory: the configured ring wins over the primary key that
	// the provider constructor happens to pass along.
	opts = ProviderOptions{Keys: NewKeyring("k1", "k2")}
	ring := opts.Keyring("k1")
	require.True(t, ring.Rotates())
	assert.Equal(t, []string{"k1", "k2", "k1"}, []string{ring.Next(), ring.Next(), ring.Next()})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
