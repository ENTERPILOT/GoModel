package providers

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyringReplace(t *testing.T) {
	ring := NewKeyring("k1", "k2")
	require.True(t, ring.Replace("k3", "", "k4", "k3"))
	assert.Equal(t, 2, ring.Len())
	got := []string{ring.Next(), ring.Next()}
	assert.ElementsMatch(t, []string{"k3", "k4"}, got)
	assert.Equal(t, "k3", ring.Primary())

	t.Run("no usable key leaves the ring unchanged", func(t *testing.T) {
		assert.False(t, ring.Replace())
		assert.False(t, ring.Replace("", ""))
		assert.Equal(t, 2, ring.Len())
		assert.Equal(t, "k3", ring.Primary())
	})

	t.Run("nil ring", func(t *testing.T) {
		var empty *Keyring
		assert.False(t, empty.Replace("k1"))
		assert.Empty(t, empty.Next())
	})

	t.Run("single key ring grows into rotation", func(t *testing.T) {
		single := NewKeyring("only")
		require.False(t, single.Rotates())
		require.True(t, single.Replace("a", "b"))
		assert.True(t, single.Rotates())
	})
}

func TestKeyringReplaceKeepsUnaffectedSessionsPinned(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")
	before := make(map[string]string)
	for i := range 200 {
		session := "session-" + strconv.Itoa(i)
		before[session] = ring.NextForSession(session)
	}
	// Rotate k2 out for k4. Sessions pinned to k1 or k3 move only if k4 now
	// outscores their key; sessions on k2 must move.
	require.True(t, ring.Replace("k1", "k4", "k3"))
	moved := 0
	for session, key := range before {
		after := ring.NextForSession(session)
		if key == "k2" {
			assert.NotEqual(t, "k2", after)
			continue
		}
		if after != key {
			assert.Equal(t, "k4", after, "a session may only move to the new key")
			moved++
		}
	}
	assert.Less(t, moved, len(before)/2)
}

func TestKeyringReplaceConcurrentWithReads(t *testing.T) {
	sets := [][]string{{"a1", "a2"}, {"b1", "b2", "b3"}, {"c1"}}
	valid := slices.Concat(sets...)
	ring := NewKeyring(sets[0]...)
	ctx := core.WithSessionID(context.Background(), "conversation-1")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				assert.Contains(t, valid, ring.Next())
				assert.Contains(t, valid, ring.NextForContext(ctx))
				assert.Contains(t, valid, ring.Primary())
				if key, ok := ring.StableForContext(ctx); ok {
					assert.Contains(t, valid, key)
				}
				assert.Positive(t, ring.Len())
			}
		})
	}
	for i := range 1000 {
		require.True(t, ring.Replace(sets[i%len(sets)]...))
	}
	close(stop)
	wg.Wait()
}

func TestKeyringNextDoesNotAllocate(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = ring.Next() }))
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = ring.NextForContext(context.Background()) }))
}
