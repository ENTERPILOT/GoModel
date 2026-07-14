package responsecache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/cache"
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
		if m.UsesRedis() {
			t.Error("UsesRedis() = true, want false")
		}
		if err := m.Ping(context.Background()); err != nil {
			t.Errorf("Ping() = %v, want nil", err)
		}
	})

	t.Run("non-pinger store is not a readiness component", func(t *testing.T) {
		m := NewResponseCacheMiddlewareWithStore(cache.NewMapStore(), time.Minute)
		if m.UsesRedis() {
			t.Error("UsesRedis() = true for in-memory store, want false")
		}
		if err := m.Ping(context.Background()); err != nil {
			t.Errorf("Ping() = %v, want nil", err)
		}
	})

	t.Run("pinger store reachable", func(t *testing.T) {
		m := NewResponseCacheMiddlewareWithStore(pingableStore{MapStore: cache.NewMapStore()}, time.Minute)
		if !m.UsesRedis() {
			t.Error("UsesRedis() = false for pinger store, want true")
		}
		if err := m.Ping(context.Background()); err != nil {
			t.Errorf("Ping() = %v, want nil", err)
		}
	})

	t.Run("pinger store down", func(t *testing.T) {
		want := errors.New("redis down")
		m := NewResponseCacheMiddlewareWithStore(pingableStore{MapStore: cache.NewMapStore(), err: want}, time.Minute)
		if !m.UsesRedis() {
			t.Error("UsesRedis() = false for pinger store, want true")
		}
		if err := m.Ping(context.Background()); !errors.Is(err, want) {
			t.Errorf("Ping() = %v, want %v", err, want)
		}
	})
}
