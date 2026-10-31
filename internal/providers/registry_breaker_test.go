package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// listingClientProvider lists models through a real llmclient, so the shared
// circuit breaker sits between the registry and the upstream.
type listingClientProvider struct {
	*registryMockProvider
	client *llmclient.Client
}

func (p *listingClientProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var resp core.ModelsResponse
	err := p.client.Do(ctx, llmclient.Request{Method: http.MethodGet, Endpoint: "/models"}, &resp)
	return &resp, err
}

func (p *listingClientProvider) CheckAvailability(ctx context.Context) error {
	_, err := p.ListModels(ctx)
	return err
}

// A provider whose /models endpoint fails while inference works must keep
// serving inference, however many discovery rounds fail.
func TestFailedDiscoveryDoesNotOpenInferenceCircuitBreaker(t *testing.T) {
	const rounds = 5
	tests := []struct {
		name  string
		round func(context.Context, *ModelRegistry, *listingClientProvider)
	}{
		{"inventory refresh", func(ctx context.Context, registry *ModelRegistry, _ *listingClientProvider) {
			assert.Error(t, registry.Refresh(ctx))
		}},
		{"availability probe", func(ctx context.Context, registry *ModelRegistry, provider *listingClientProvider) {
			assert.Error(t, registry.probeAvailability(ctx, provider, "test", time.Second))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var listings, chats atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					listings.Add(1)
					http.Error(w, "Error listing serverless models", http.StatusInternalServerError)
					return
				}
				chats.Add(1)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			cfg := llmclient.DefaultConfig("test", server.URL)
			cfg.Retry.MaxRetries = 0
			cfg.CircuitBreaker.FailureThreshold = 2
			provider := &listingClientProvider{
				registryMockProvider: &registryMockProvider{name: "test"},
				client:               llmclient.New(cfg, nil),
			}
			registry := NewModelRegistry()
			registry.RegisterProvider(provider)

			ctx := context.Background()
			for range rounds {
				tt.round(ctx, registry, provider)
			}
			require.EqualValues(t, rounds, listings.Load())

			var out map[string]any
			err := provider.client.Do(ctx, llmclient.Request{
				Method:   http.MethodPost,
				Endpoint: "/chat/completions",
				Body:     map[string]any{"model": "m1"},
			}, &out)
			require.NoError(t, err)
			assert.EqualValues(t, 1, chats.Load())
		})
	}
}
