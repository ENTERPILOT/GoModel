package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"gomodel/internal/cache/modelcache"
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
	for i := 0; i < goroutines; i++ {
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
