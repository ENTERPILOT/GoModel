package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache/modelcache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

type mockInitCache struct {
	closeCalls atomic.Int32
	closeErr   error
}

func (m *mockInitCache) Get(context.Context) (*modelcache.ModelCache, error) {
	return nil, nil
}

func (m *mockInitCache) Set(context.Context, *modelcache.ModelCache) error {
	return nil
}

func (m *mockInitCache) Close() error {
	m.closeCalls.Add(1)
	return m.closeErr
}

func TestInitResultClose_IsIdempotentAndConcurrentSafe(t *testing.T) {
	cacheErr := errors.New("cache close failed")
	cache := &mockInitCache{closeErr: cacheErr}

	var stopCalls atomic.Int32
	result := &InitResult{
		Cache: cache,
		stopRefresh: func() {
			stopCalls.Add(1)
		},
	}

	const goroutines = 8
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			errs <- result.Close()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.ErrorIs(t, err, cacheErr)
	}
	require.Equal(t, int32(1), stopCalls.Load())
	require.Equal(t, int32(1), cache.closeCalls.Load())
}

func TestInitResultClose_NilReceiver(t *testing.T) {
	var result *InitResult
	err := result.Close()
	require.NoError(t, err)
}

type initTestProvider struct {
	availabilityErr   error
	checkAvailability func(context.Context) error
	listModelsErr     error
	modelsResponse    *core.ModelsResponse
}

func (p *initTestProvider) CheckAvailability(ctx context.Context) error {
	if p.checkAvailability != nil {
		return p.checkAvailability(ctx)
	}
	return p.availabilityErr
}

func (p *initTestProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return &core.ChatResponse{}, nil
}

func (p *initTestProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (p *initTestProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	if p.listModelsErr != nil {
		return nil, p.listModelsErr
	}
	if p.modelsResponse != nil {
		return p.modelsResponse, nil
	}
	return &core.ModelsResponse{Object: "list"}, nil
}

func (p *initTestProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return &core.ResponsesResponse{}, nil
}

func (p *initTestProvider) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (p *initTestProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return &core.EmbeddingResponse{}, nil
}

func TestInit_AllowsStartupWhenProviderIsUnavailable(t *testing.T) {
	ctx := t.Context()
	provider := &initTestProvider{
		availabilityErr: errors.New("startup unavailable"),
		listModelsErr:   errors.New("models unavailable"),
	}

	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(ProviderConfig, ProviderOptions) core.Provider {
			return provider
		},
	})

	result, err := Init(ctx, &config.LoadResult{
		Config: &config.Config{
			Cache: config.CacheConfig{
				Model: config.ModelCacheConfig{
					RefreshInterval: 1,
					Local: &config.LocalCacheConfig{
						CacheDir: t.TempDir(),
					},
				},
			},
		},
		RawProviders: map[string]config.RawProviderConfig{
			"test": {
				Type:   "test",
				APIKey: "sk-test",
			},
		},
	}, factory)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = result.Close()
	})
	require.Equal(t, 1, result.Registry.ProviderCount())
	require.Same(t, provider, result.Registry.ProviderByType("test"), "unavailable provider should still be registered")
}

// TestInit_SucceedsWithNoProvidersConfigured verifies GoModel can boot with
// zero env var/config.yaml providers (e.g. all credentials come from the
// dashboard's provider-credentials store instead), rather than failing
// startup as it did before that store existed.
func TestInit_SucceedsWithNoProvidersConfigured(t *testing.T) {
	ctx := t.Context()
	factory := NewProviderFactory()

	result, err := Init(ctx, &config.LoadResult{
		Config: &config.Config{
			Cache: config.CacheConfig{
				Model: config.ModelCacheConfig{
					RefreshInterval: 1,
					Local: &config.LocalCacheConfig{
						CacheDir: t.TempDir(),
					},
				},
			},
		},
		RawProviders: map[string]config.RawProviderConfig{},
	}, factory)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = result.Close()
	})
	got := result.Registry.ProviderCount()
	require.Equal(t, 0, got)
	got = result.Registry.ModelCount()
	require.Equal(t, 0, got)
	require.NotNil(t, result.Router)
}

func TestInit_NormalizesNilContext(t *testing.T) {
	nilInitContext := func() context.Context {
		return nil
	}

	cacheDir, err := os.MkdirTemp("", "gomodel-init-nil-context-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(cacheDir)
	})

	modelListFetched := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case modelListFetched <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":1,"updated_at":"2026-04-10T00:00:00Z","providers":{},"models":{},"provider_models":{}}`)
	}))
	defer server.Close()

	provider := &initTestProvider{
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "test-model", Object: "model", OwnedBy: "test"},
			},
		},
	}

	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(ProviderConfig, ProviderOptions) core.Provider {
			return provider
		},
	})

	result, err := Init(nilInitContext(), &config.LoadResult{
		Config: &config.Config{
			Cache: config.CacheConfig{
				Model: config.ModelCacheConfig{
					RefreshInterval: 1,
					ModelList: config.ModelListConfig{
						URL: server.URL,
					},
					Local: &config.LocalCacheConfig{
						CacheDir: cacheDir,
					},
				},
			},
		},
		RawProviders: map[string]config.RawProviderConfig{
			"test": {
				Type:   "test",
				APIKey: "sk-test",
			},
		},
	}, factory)
	require.NoError(t, err)

	defer func() {
		_ = result.Close()
	}()

	select {
	case <-modelListFetched:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Init(nil, ...) to fetch the model list without a nil-context panic")
	}

	cacheFile := filepath.Join(cacheDir, "models.json")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if result.Registry.IsInitialized() {
			if _, err := os.Stat(cacheFile); err == nil {
				if _, err := os.Stat(cacheFile + ".tmp"); os.IsNotExist(err) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.True(t, result.Registry.IsInitialized())
	_, err = os.Stat(cacheFile)
	require.NoError(t, err)
	_, err = os.Stat(cacheFile + ".tmp")
	require.True(t, os.IsNotExist(err))
}

const unreachableRedisURL = "redis://127.0.0.1:1"

func TestInitCache_FallsBackToLocalWhenRedisUnreachable(t *testing.T) {
	cacheDir := t.TempDir()
	c, err := initCache(&config.Config{
		Cache: config.CacheConfig{
			Model: config.ModelCacheConfig{
				Redis: &config.RedisModelConfig{URL: unreachableRedisURL},
				Local: &config.LocalCacheConfig{CacheDir: cacheDir},
			},
		},
	})
	require.NoError(t, err)

	t.Cleanup(func() { _ = c.Close() })
	_, ok := c.(*modelcache.LocalCache)
	require.True(t, ok, "initCache() type = %T, want *modelcache.LocalCache", c)

	ctx := t.Context()
	want := &modelcache.ModelCache{
		Providers: map[string]modelcache.CachedProvider{
			"test": {ProviderType: "test", OwnedBy: "test"},
		},
	}
	err = c.Set(ctx, want)
	require.NoError(t, err)

	got, err := c.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "test", got.Providers["test"].ProviderType)
}

func TestInitCache_FailsWhenRedisUnreachableAndNoLocal(t *testing.T) {
	c, err := initCache(&config.Config{
		Cache: config.CacheConfig{
			Model: config.ModelCacheConfig{
				Redis: &config.RedisModelConfig{URL: unreachableRedisURL},
			},
		},
	})
	if c != nil {
		_ = c.Close()
		t.Fatal("initCache() cache != nil, want nil when redis is the only backend and is unreachable")
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to connect to redis")
}

func TestInit_SucceedsWhenRedisUnreachableAndLocalConfigured(t *testing.T) {
	result, err := Init(t.Context(), &config.LoadResult{
		Config: &config.Config{
			Cache: config.CacheConfig{
				Model: config.ModelCacheConfig{
					RefreshInterval: 1,
					Redis:           &config.RedisModelConfig{URL: unreachableRedisURL},
					Local:           &config.LocalCacheConfig{CacheDir: t.TempDir()},
				},
			},
		},
		RawProviders: map[string]config.RawProviderConfig{},
	}, NewProviderFactory())
	require.NoError(t, err)

	t.Cleanup(func() { _ = result.Close() })
	require.NotNil(t, result.Router)
	_, ok := result.Cache.(*modelcache.LocalCache)
	require.True(t, ok, "Cache type = %T, want *modelcache.LocalCache", result.Cache)
}

func TestInitializeProviders_UnavailableProviderCanRefreshLater(t *testing.T) {
	ctx := t.Context()
	provider := &initTestProvider{
		availabilityErr: errors.New("startup unavailable"),
		listModelsErr:   errors.New("models unavailable"),
	}

	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(ProviderConfig, ProviderOptions) core.Provider {
			return provider
		},
	})

	registry := NewModelRegistry()
	count, err := initializeProviders(ctx, map[string]ProviderConfig{
		"test": {Type: "test", APIKey: "sk-test"},
	}, factory, registry)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Error(t, registry.Refresh(ctx))

	provider.listModelsErr = nil
	provider.modelsResponse = &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{ID: "later-model", Object: "model", OwnedBy: "test"},
		},
	}
	err = registry.Refresh(ctx)
	require.NoError(t, err)
	require.True(t, registry.Supports("later-model"))
}

func TestInitializeProviders_AvailabilityCheckUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var checkErr error
	provider := &initTestProvider{
		checkAvailability: func(ctx context.Context) error {
			checkErr = ctx.Err()
			return ctx.Err()
		},
	}

	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(ProviderConfig, ProviderOptions) core.Provider {
			return provider
		},
	})

	registry := NewModelRegistry()
	count, err := initializeProviders(ctx, map[string]ProviderConfig{
		"test": {Type: "test", APIKey: "sk-test"},
	}, factory, registry)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.ErrorIs(t, checkErr, context.Canceled)
}

func TestInitializeProviders_ParallelizesProbesAndRegistersDeterministically(t *testing.T) {
	const providerCount = 3
	var started atomic.Int32
	var startedOnce sync.Once
	allStarted := make(chan struct{})
	release := make(chan struct{})
	providers := make(map[string]*initTestProvider, providerCount)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		providers[name] = &initTestProvider{
			checkAvailability: func(context.Context) error {
				if started.Add(1) == providerCount {
					startedOnce.Do(func() { close(allStarted) })
				}
				<-release
				if name == "beta" {
					return errors.New("beta unavailable")
				}
				return nil
			},
		}
	}

	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, _ ProviderOptions) core.Provider {
			return providers[cfg.Name]
		},
	})
	providerConfigs := make(map[string]ProviderConfig, providerCount)
	for name := range providers {
		providerConfigs[name] = ProviderConfig{Name: name, Type: "test"}
	}

	registry := NewModelRegistry()
	type result struct {
		count int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		count, err := initializeProviders(t.Context(), providerConfigs, factory, registry)
		done <- result{count: count, err: err}
	}()

	select {
	case <-allStarted:
	case <-time.After(time.Second):
		t.Fatal("availability checks did not overlap")
	}
	close(release)

	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, providerCount, got.count)

	case <-time.After(time.Second):
		t.Fatal("initializeProviders() did not finish")
	}
	got := registry.providerNames[registry.providers[0]]
	require.Equal(t, "alpha", got)
	got = registry.providerNames[registry.providers[1]]
	require.Equal(t, "beta", got)
	got = registry.providerNames[registry.providers[2]]
	require.Equal(t, "gamma", got)
	got = registry.providerRuntime["beta"].lastAvailabilityError
	require.Equal(t, "beta unavailable", got)
}

func TestInitializeProviders_DoesNotLaunchUnboundedWorkers(t *testing.T) {
	const providerCount = 16
	const maxWorkers = 8
	started := make(chan struct{}, providerCount)
	release := make(chan struct{})
	providers := make(map[string]*initTestProvider, providerCount)
	for i := range providerCount {
		name := fmt.Sprintf("provider-%02d", i)
		providers[name] = &initTestProvider{
			checkAvailability: func(context.Context) error {
				started <- struct{}{}
				<-release
				return nil
			},
		}
	}

	factory := NewProviderFactory()
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, _ ProviderOptions) core.Provider {
			return providers[cfg.Name]
		},
	})
	providerConfigs := make(map[string]ProviderConfig, providerCount)
	for name := range providers {
		providerConfigs[name] = ProviderConfig{Name: name, Type: "test"}
	}

	done := make(chan struct{})
	go func() {
		_, _ = initializeProviders(t.Context(), providerConfigs, factory, NewModelRegistry())
		close(done)
	}()

	for range maxWorkers {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("provider initialization did not start all workers")
		}
	}

	select {
	case <-started:
		t.Fatal("initializeProviders launched more than the worker limit")
	default:
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("initializeProviders did not finish")
	}

	require.Equal(t, providerCount-maxWorkers, len(started))
}
