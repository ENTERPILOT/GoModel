package providers

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"gomodel/internal/core"
)

// slowMockProvider simulates network latency to provoke race conditions
type slowMockProvider struct {
	delay time.Duration
}

func (m *slowMockProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(m.delay):
	}
	return &core.ModelsResponse{
		Data: []core.Model{{ID: "slow-model", OwnedBy: "test"}},
	}, nil
}

// Implement other interface methods as no-ops with CORRECT Go types
func (m *slowMockProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (m *slowMockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *slowMockProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (m *slowMockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *slowMockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

func (m *slowMockProvider) Supports(model string) bool {
	return true
}

func TestRegistry_Concurrency(t *testing.T) {
	registry := NewModelRegistry()
	// Register a provider that takes 10ms to respond
	registry.RegisterProvider(&slowMockProvider{delay: 10 * time.Millisecond})

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Background Refresher (Writer)
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = registry.Refresh(context.Background())
				time.Sleep(15 * time.Millisecond)
			}
		}
	})

	// 2. Heavy Readers
	for range 50 {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					_ = registry.GetProvider("slow-model")
					_ = registry.ListModels()
					_ = registry.Supports("slow-model")
					time.Sleep(1 * time.Millisecond)
				}
			}
		})
	}

	// Let it run for 1 second
	time.Sleep(1 * time.Second)
	cancel()
	wg.Wait()
}
