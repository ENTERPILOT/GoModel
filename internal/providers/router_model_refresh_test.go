package providers

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// countingRefreshProvider counts upstream model listings and can hold them
// until release is closed, so concurrent refreshes overlap.
type countingRefreshProvider struct {
	mockProvider
	listModelsCalls atomic.Int32
	release         chan struct{}

	mu     sync.Mutex
	models []string
}

func (p *countingRefreshProvider) setModels(ids ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = ids
}

func (p *countingRefreshProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	p.listModelsCalls.Add(1)
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	resp := &core.ModelsResponse{Object: "list"}
	for _, id := range p.models {
		resp.Data = append(resp.Data, core.Model{ID: id, Object: "model", OwnedBy: "ollama"})
	}
	return resp, nil
}

func (p *countingRefreshProvider) CheckAvailability(context.Context) error { return nil }

func newOnDemandRefreshRouter(t *testing.T, provider *countingRefreshProvider) *Router {
	t.Helper()
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "ollama", "ollama")
	router, err := NewRouter(registry)
	require.NoError(t, err)
	return router
}

func requireModelNotFound(t *testing.T, err error) {
	t.Helper()
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	require.True(t, ok, "expected gateway error, got %v", err)
	assert.Equal(t, http.StatusNotFound, gatewayErr.HTTPStatusCode())
}

func TestRouterOnDemandRefresh_UnknownModelFetchesOncePerCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &countingRefreshProvider{name: "ollama"}
		provider.setModels("known-model")
		router := newOnDemandRefreshRouter(t, provider)

		for range 5 {
			_, err := router.ChatCompletion(t.Context(), &core.ChatRequest{Model: "ollama/typo-model"})
			requireModelNotFound(t, err)
		}
		_, err := router.RefreshProviderModels(t.Context(), "ollama")
		require.NoError(t, err)
		assert.Equal(t, int32(1), provider.listModelsCalls.Load())
	})
}

func TestRouterOnDemandRefresh_ConcurrentMissesShareOneFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &countingRefreshProvider{
			name:    "ollama",
			release: make(chan struct{}),
		}
		provider.setModels("known-model")
		router := newOnDemandRefreshRouter(t, provider)

		const requests = 10
		errs := make([]error, requests)
		var wg sync.WaitGroup
		for i := range requests {
			wg.Go(func() {
				_, errs[i] = router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/typo-model"})
			})
		}
		synctest.Wait()
		close(provider.release)
		wg.Wait()

		for _, err := range errs {
			requireModelNotFound(t, err)
		}
		assert.Equal(t, int32(1), provider.listModelsCalls.Load())
	})
}

func TestRouterOnDemandRefresh_ModelAddedUpstreamRoutesAfterCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &countingRefreshProvider{
			name:         "ollama",
			chatResponse: &core.ChatResponse{ID: "chatcmpl-new", Model: "new-model"},
		}
		provider.setModels("known-model")
		router := newOnDemandRefreshRouter(t, provider)

		_, err := router.ChatCompletion(t.Context(), &core.ChatRequest{Model: "ollama/new-model"})
		requireModelNotFound(t, err)

		provider.setModels("known-model", "new-model")
		time.Sleep(onDemandRefreshCooldown - time.Second)
		_, err = router.ChatCompletion(t.Context(), &core.ChatRequest{Model: "ollama/new-model"})
		requireModelNotFound(t, err)
		require.Equal(t, int32(1), provider.listModelsCalls.Load())

		time.Sleep(time.Second)
		resp, err := router.ChatCompletion(t.Context(), &core.ChatRequest{Model: "ollama/new-model"})
		require.NoError(t, err)
		assert.Equal(t, "chatcmpl-new", resp.ID)
		assert.Equal(t, int32(2), provider.listModelsCalls.Load())
	})
}

func TestRouterOnDemandRefresh_CanceledCallerDoesNotFailSharedFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &countingRefreshProvider{
			name:    "ollama",
			release: make(chan struct{}),
		}
		provider.setModels("known-model")
		router := newOnDemandRefreshRouter(t, provider)

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		var leaderErr, followerErr error
		var followerCount int
		var wg sync.WaitGroup
		wg.Go(func() {
			_, leaderErr = router.RefreshProviderModels(leaderCtx, "ollama")
		})
		synctest.Wait()
		wg.Go(func() {
			followerCount, followerErr = router.RefreshProviderModels(context.Background(), "ollama")
		})
		synctest.Wait()

		cancelLeader()
		synctest.Wait()
		close(provider.release)
		wg.Wait()

		gatewayErr, ok := errors.AsType[*core.GatewayError](leaderErr)
		require.True(t, ok, "expected gateway error, got %v", leaderErr)
		assert.Equal(t, http.StatusRequestTimeout, gatewayErr.HTTPStatusCode())
		require.NoError(t, followerErr)
		assert.Equal(t, 1, followerCount)
		assert.Equal(t, int32(1), provider.listModelsCalls.Load())
	})
}

func TestRouterOnDemandRefresh_NilContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		provider := &countingRefreshProvider{mockProvider: mockProvider{name: "ollama"}}
		provider.setModels("known-model")
		router := newOnDemandRefreshRouter(t, provider)

		var ctx context.Context
		count, err := router.RefreshProviderModels(ctx, "ollama")
		require.NoError(t, err)
		assert.Equal(t, 1, count)
	})
}
