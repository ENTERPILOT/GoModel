package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gomodel/config"
	"gomodel/internal/cache/modelcache"
	"gomodel/internal/core"
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
		if !errors.Is(err, cacheErr) {
			t.Fatalf("Close() error = %v, want %v", err, cacheErr)
		}
	}
	if stopCalls.Load() != 1 {
		t.Fatalf("stopRefresh called %d times, want 1", stopCalls.Load())
	}
	if cache.closeCalls.Load() != 1 {
		t.Fatalf("cache.Close called %d times, want 1", cache.closeCalls.Load())
	}
}

func TestInitResultClose_NilReceiver(t *testing.T) {
	var result *InitResult
	if err := result.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
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
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = result.Close()
	})

	if got := result.Registry.ProviderCount(); got != 1 {
		t.Fatalf("ProviderCount() = %d, want 1", got)
	}
	if got := result.Registry.ProviderByType("test"); got != provider {
		t.Fatal("ProviderByType(test) = nil or wrong provider, want registered unavailable provider")
	}
}

func TestInit_NormalizesNilContext(t *testing.T) {
	nilInitContext := func() context.Context {
		return nil
	}

	cacheDir, err := os.MkdirTemp("", "gomodel-init-nil-context-*")
	if err != nil {
		t.Fatalf("os.MkdirTemp() error = %v, want nil", err)
	}
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
	if err != nil {
		t.Fatalf("Init() error = %v, want nil", err)
	}
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

	if !result.Registry.IsInitialized() {
		t.Fatal("expected Init(nil, ...) to complete background registry initialization")
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("expected Init(nil, ...) to persist the cache file, stat error = %v", err)
	}
	if _, err := os.Stat(cacheFile + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected no in-progress cache temp file, stat error = %v", err)
	}
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
	if err != nil {
		t.Fatalf("initializeProviders() error = %v, want nil", err)
	}
	if count != 1 {
		t.Fatalf("initializeProviders() count = %d, want 1", count)
	}

	if err := registry.Refresh(ctx); err == nil {
		t.Fatal("Refresh() error = nil, want startup failure while provider models are unavailable")
	}

	provider.listModelsErr = nil
	provider.modelsResponse = &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{ID: "later-model", Object: "model", OwnedBy: "test"},
		},
	}

	if err := registry.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() after recovery error = %v, want nil", err)
	}
	if !registry.Supports("later-model") {
		t.Fatal("expected later-model to be discoverable after refresh")
	}
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
	if err != nil {
		t.Fatalf("initializeProviders() error = %v, want nil", err)
	}
	if count != 1 {
		t.Fatalf("initializeProviders() count = %d, want 1", count)
	}
	if !errors.Is(checkErr, context.Canceled) {
		t.Fatalf("CheckAvailability() context error = %v, want %v", checkErr, context.Canceled)
	}
}
