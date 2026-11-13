package responsecache

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func contextWithCeilings(effort string, tokens int) context.Context {
	return core.WithRequestCeilings(context.Background(), core.RequestCeilings{ReasoningEffort: effort, OutputTokens: tokens})
}

// A provider adapter applies the caller's ceilings after the body the key is
// built from, so the key carries them: one body under two ceilings is two
// entries, and a request without ceilings keeps the key it always had.
func TestExactCacheKey_ScopedToRequestCeilings(t *testing.T) {
	tests := []struct {
		name     string
		first    context.Context
		second   context.Context
		wantSame bool
	}{
		{name: "no ceilings keep the unscoped key", first: context.Background(), second: contextWithCeilings("", 0), wantSame: true},
		{name: "the same ceilings share a key", first: contextWithCeilings("low", 1000), second: contextWithCeilings("low", 1000), wantSame: true},
		{name: "a token ceiling is not the uncapped key", first: context.Background(), second: contextWithCeilings("", 1000)},
		{name: "an effort ceiling is not the uncapped key", first: context.Background(), second: contextWithCeilings("low", 0)},
		{name: "token ceilings differ", first: contextWithCeilings("", 1000), second: contextWithCeilings("", 8000)},
		{name: "effort ceilings differ", first: contextWithCeilings("low", 1000), second: contextWithCeilings("high", 1000)},
	}
	body := []byte(partitionChatBody)
	for _, partition := range []bool{false, true} {
		m := &simpleCacheMiddleware{partitionByUserPath: partition}
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s (partitioned by user path: %t)", tt.name, partition), func(t *testing.T) {
				first := m.exactCacheKey(&blockingMissExchange{ctx: tt.first}, body)
				second := m.exactCacheKey(&blockingMissExchange{ctx: tt.second}, body)
				if tt.wantSame {
					assert.Equal(t, first, second)
				} else {
					assert.NotEqual(t, first, second)
				}
			})
		}
	}
}

// Both layers scope their entries: an answer stored without ceilings, or
// under other ceilings, is never replayed to a capped request.
func TestHandleRequest_CeilingsScopeEveryLayer(t *testing.T) {
	for _, layer := range []struct {
		name            string
		exact, semantic bool
		hit             string
	}{
		{name: "exact", exact: true, hit: CacheHeaderExact},
		{name: "semantic", semantic: true, hit: CacheHeaderSemantic},
	} {
		t.Run(layer.name, func(t *testing.T) {
			mw, _ := partitionedMiddleware(t, layer.exact, layer.semantic, false)
			serve := func(ctx context.Context) string {
				t.Helper()
				c, rec := echotest.Post(t, partitionChatPath, partitionChatBody)
				c.SetRequest(c.Request().WithContext(ctx))
				require.NoError(t, mw.HandleRequest(c, []byte(partitionChatBody), func() error {
					return c.JSON(http.StatusOK, map[string]string{"answer": "ok"})
				}))
				flushCacheWrites(mw)
				return rec.Header().Get("X-Cache")
			}

			require.Empty(t, serve(context.Background()))
			require.Equal(t, layer.hit, serve(context.Background()))
			assert.Empty(t, serve(contextWithCeilings("", 1000)), "an uncapped answer is not replayed under a ceiling")
			assert.Equal(t, layer.hit, serve(contextWithCeilings("", 1000)))
			assert.Empty(t, serve(contextWithCeilings("low", 1000)), "nor is one stored under other ceilings")
			assert.Equal(t, layer.hit, serve(context.Background()), "the uncapped entry is still there")
		})
	}
}
