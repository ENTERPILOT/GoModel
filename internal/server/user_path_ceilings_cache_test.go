package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/users"
)

// writeCountingStore counts finished writes, which the cache makes
// asynchronously.
type writeCountingStore struct {
	cache.Store
	writes atomic.Int32
}

func (s *writeCountingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	err := s.Store.Set(ctx, key, value, ttl)
	s.writes.Add(1)
	return err
}

// The Anthropic adapter applies the ceilings after the request the cache key
// is built from: this body is the same before and after a token ceiling of
// 1000, yet without the ceiling the adapter raises max_tokens above the
// thinking budget, and with it keeps 1000 and turns thinking off. An answer
// cached before the ceiling was set must not be served once it is.
func TestResponseCacheDoesNotServeAnAnswerFromBeforeACeiling(t *testing.T) {
	const body = `{"model":"anthropic/claude-sonnet-4-5","max_tokens":1000,"reasoning_effort":"high",` + hiMessages + `}`
	store := &writeCountingStore{Store: cache.NewMapStore()}
	mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	t.Cleanup(func() { _ = mw.Close() })
	f := newUpstreamFixture(t, nil, nil)
	f.handler.responseCache = mw
	misses := int32(0)
	send := func() string {
		t.Helper()
		rec := f.post(t, "/team", "/v1/chat/completions", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		served := rec.Header().Get("X-Cache")
		if served == "" {
			misses++
			require.Eventually(t, func() bool { return store.writes.Load() == misses }, 5*time.Second, time.Millisecond, "the answer is cached")
		}
		return served
	}

	require.Empty(t, send())
	uncapped := f.sent(t, "anthropic")
	assert.Greater(t, number(t, uncapped, "max_tokens"), 1000, "the adapter makes room above the thinking budget")
	assert.Contains(t, uncapped, "thinking")
	require.Equal(t, responsecache.CacheHeaderExact, send(), "the same request is served from the cache")

	f.setRows(t, []users.User{{UserPath: "/team", MaxOutputTokens: 1000}})
	assert.Empty(t, send(), "a ceiling set since must not be answered from the entry it did not bound")
	require.Equal(t, 2, f.upstreams["anthropic"].Count())
	capped := f.sent(t, "anthropic")
	assert.Equal(t, 1000, number(t, capped, "max_tokens"))
	assert.NotContains(t, capped, "thinking")
	assert.Equal(t, responsecache.CacheHeaderExact, send(), "the capped answer is cached under its own ceilings")

	f.setRows(t, []users.User{{UserPath: "/team", MaxOutputTokens: 8000}})
	assert.Empty(t, send(), "another ceiling is another entry")
	assert.Equal(t, 3, f.upstreams["anthropic"].Count())
}
