package responsecache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/stretchr/testify/assert"
)

// pingableStore is a cache.Store that also implements cache.Pinger.
type pingableStore struct {
	*cache.MapStore
	err error
}

func (p pingableStore) Ping(context.Context) error { return p.err }

func TestUsesRedisAndPing(t *testing.T) {
	t.Run("nil middleware", func(t *testing.T) {
		var m *ResponseCacheMiddleware
		assert.False(t, m.UsesRedis())
		err := m.Ping(context.Background())
		assert.NoError(t, err)
	})

	t.Run("non-pinger store is not a readiness component", func(t *testing.T) {
		m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Minute)
		assert.False(t, m.UsesRedis())
		err := m.Ping(context.Background())
		assert.NoError(t, err)
	})

	t.Run("pinger store reachable", func(t *testing.T) {
		m := NewResponseCacheMiddlewareWithStore(pingableStore{MapStore: cache.NewMapStore()}, time.Minute)
		assert.True(t, m.UsesRedis())
		err := m.Ping(context.Background())
		assert.NoError(t, err)
	})

	t.Run("pinger store down", func(t *testing.T) {
		want := errors.New("redis down")
		m := NewResponseCacheMiddlewareWithStore(pingableStore{MapStore: cache.NewMapStore(), err: want}, time.Minute)
		assert.True(t, m.UsesRedis())
		err := m.Ping(context.Background())
		assert.ErrorIs(t, err, want)
	})
}
