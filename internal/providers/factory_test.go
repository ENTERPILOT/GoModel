package providers

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ ProviderConstructor = func(_ ProviderConfig, _ ProviderOptions) core.Provider { return nil }

type factoryMockProvider struct {
	supportsFunc func(model string) bool
}

func (m *factoryMockProvider) Supports(model string) bool {
	if m.supportsFunc != nil {
		return m.supportsFunc(model)
	}
	return true
}

func (m *factoryMockProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return &core.ChatResponse{}, nil
}

func (m *factoryMockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *factoryMockProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{}, nil
}

func (m *factoryMockProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return &core.ResponsesResponse{}, nil
}

func (m *factoryMockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *factoryMockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return &core.EmbeddingResponse{}, nil
}

func TestProviderFactory_Register(t *testing.T) {
	factory := NewProviderFactory()

	factory.Add(Registration{
		Type: "test-provider",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			return &factoryMockProvider{}
		},
	})

	registered := factory.RegisteredTypes()
	require.Len(t, registered, 1)
	assert.Equal(t, "test-provider", registered[0])
}

func TestProviderFactory_Add_PanicsOnEmptyType(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r)

	}()
	NewProviderFactory().Add(Registration{
		Type: "",
		New:  func(_ ProviderConfig, _ ProviderOptions) core.Provider { return nil },
	})
}

func TestProviderFactory_Add_PanicsOnNilConstructor(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r)

	}()
	NewProviderFactory().Add(Registration{Type: "test", New: nil})
}

func TestProviderFactory_Create_UnknownType(t *testing.T) {
	factory := NewProviderFactory()

	cfg := ProviderConfig{
		Type:   "unknown-type",
		APIKey: "test-key",
	}

	_, err := factory.Create(cfg)
	require.EqualError(t, err, "unknown provider type: unknown-type")
}

func TestProviderFactory_Create_Success(t *testing.T) {
	factory := NewProviderFactory()

	factory.Add(Registration{
		Type: "mock",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			return &factoryMockProvider{}
		},
	})

	cfg := ProviderConfig{
		Type:   "mock",
		APIKey: "test-key",
	}

	provider, err := factory.Create(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, provider)
}

func TestProviderFactory_RegisteredTypes(t *testing.T) {
	factory := NewProviderFactory()

	for _, name := range []string{"provider1", "provider2", "provider3"} {
		factory.Add(Registration{
			Type: name,
			New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
				return &factoryMockProvider{}
			},
		})
	}

	registered := factory.RegisteredTypes()

	assert.Len(t, registered, 3)

	found := make(map[string]bool)
	for _, name := range registered {
		found[name] = true
	}

	for _, expected := range []string{"provider1", "provider2", "provider3"} {
		assert.True(t, found[expected], "expected '%s' to be in registered list", expected)
	}
}

func TestProviderFactory_PassthroughSemanticEnrichers(t *testing.T) {
	factory := NewProviderFactory()

	factory.Add(Registration{
		Type:                        "provider-b",
		New:                         func(cfg ProviderConfig, opts ProviderOptions) core.Provider { return &factoryMockProvider{} },
		PassthroughSemanticEnricher: passthroughEnricherStub{providerType: "provider-b"},
	})
	factory.Add(Registration{
		Type:                        "provider-a",
		New:                         func(cfg ProviderConfig, opts ProviderOptions) core.Provider { return &factoryMockProvider{} },
		PassthroughSemanticEnricher: passthroughEnricherStub{providerType: "provider-a"},
	})

	enrichers := factory.PassthroughSemanticEnrichers()
	require.Len(t, enrichers, 2)
	got := enrichers[0].ProviderType()
	require.Equal(t, "provider-a", got)
	got = enrichers[1].ProviderType()
	require.Equal(t, "provider-b", got)
}

type passthroughEnricherStub struct {
	providerType string
}

func (p passthroughEnricherStub) ProviderType() string {
	return p.providerType
}

func (passthroughEnricherStub) Enrich(_ *core.RequestSnapshot, _ *core.WhiteBoxPrompt, info *core.PassthroughRouteInfo) *core.PassthroughRouteInfo {
	return info
}

func TestProviderFactory_Create_PassesResolvedProviderConfig(t *testing.T) {
	factory := NewProviderFactory()

	var receivedCfg ProviderConfig
	factory.Add(Registration{
		Type: "custom",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			receivedCfg = cfg
			return &factoryMockProvider{}
		},
	})

	cfg := ProviderConfig{
		Type:       "custom",
		APIKey:     "test-key",
		BaseURL:    "https://custom.api.endpoint.com/v1",
		APIVersion: "2025-04-01-preview",
	}

	provider, err := factory.Create(cfg)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, "test-key", receivedCfg.APIKey)
	require.Equal(t, "https://custom.api.endpoint.com/v1", receivedCfg.BaseURL)
	require.Equal(t, "2025-04-01-preview", receivedCfg.APIVersion)
}

func TestProviderFactory_SetHooks(t *testing.T) {
	factory := NewProviderFactory()
	var startName, startType, endName, endType, chunkName, chunkType string

	mockHooks := llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			startName = info.Provider
			startType = info.ProviderType
			return ctx
		},
		OnRequestEnd: func(_ context.Context, info llmclient.ResponseInfo) {
			endName, endType = info.Provider, info.ProviderType
		},
		OnStreamFirstChunk: func(_ context.Context, info llmclient.ResponseInfo) {
			chunkName, chunkType = info.Provider, info.ProviderType
		},
	}
	factory.SetHooks(mockHooks)

	var receivedOpts ProviderOptions
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			receivedOpts = opts
			return &factoryMockProvider{}
		},
	})

	cfg := ProviderConfig{
		Name:   "test-eu",
		Type:   "test",
		APIKey: "test-key",
	}

	_, err := factory.Create(cfg)
	require.NoError(t, err)
	assert.NotNil(t, receivedOpts.Hooks.OnRequestStart)

	receivedOpts.Hooks.OnRequestStart(t.Context(), llmclient.RequestInfo{})
	receivedOpts.Hooks.OnRequestEnd(t.Context(), llmclient.ResponseInfo{})
	receivedOpts.Hooks.OnStreamFirstChunk(t.Context(), llmclient.ResponseInfo{})
	require.Equal(t, "test-eu", startName)
	require.Equal(t, "test-eu", endName)
	require.Equal(t, "test-eu", chunkName)
	require.Equal(t, "test", startType)
	require.Equal(t, "test", endType)
	require.Equal(t, "test", chunkType)
}

func TestProviderFactory_ZeroHooks(t *testing.T) {
	factory := NewProviderFactory()

	var receivedOpts ProviderOptions
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			receivedOpts = opts
			return &factoryMockProvider{}
		},
	})

	cfg := ProviderConfig{
		Type:   "test",
		APIKey: "test-key",
	}

	_, err := factory.Create(cfg)
	require.NoError(t, err)
	assert.Nil(t, receivedOpts.Hooks.OnRequestStart)
	assert.Nil(t, receivedOpts.Hooks.OnRequestEnd)
	assert.Nil(t, receivedOpts.Hooks.OnStreamFirstChunk)
}

func TestProviderFactory_Create_PassesResilienceConfig(t *testing.T) {
	factory := NewProviderFactory()

	var receivedOpts ProviderOptions
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			receivedOpts = opts
			return &factoryMockProvider{}
		},
	})

	resilience := config.ResilienceConfig{
		Retry: config.RetryConfig{
			MaxRetries:     7,
			InitialBackoff: 2 * time.Second,
			MaxBackoff:     60 * time.Second,
			BackoffFactor:  3.0,
			JitterFactor:   0.5,
		},
	}

	cfg := ProviderConfig{
		Type:       "test",
		APIKey:     "test-key",
		Resilience: resilience,
	}

	_, err := factory.Create(cfg)
	require.NoError(t, err)

	r := receivedOpts.Resilience.Retry
	assert.Equal(t, 7, r.MaxRetries)
	assert.Equal(t, 2*time.Second, r.InitialBackoff)
	assert.Equal(t, 60*time.Second, r.MaxBackoff)
	assert.Equal(t, 3.0, r.BackoffFactor)
	assert.Equal(t, 0.5, r.JitterFactor)
}

func TestProviderFactory_Create_PassesConfiguredModels(t *testing.T) {
	factory := NewProviderFactory()

	var receivedOpts ProviderOptions
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			receivedOpts = opts
			return &factoryMockProvider{}
		},
	})

	cfg := ProviderConfig{
		Type:   "test",
		APIKey: "test-key",
		Models: []string{"model-a", "model-b"},
	}

	_, err := factory.Create(cfg)
	require.NoError(t, err)
	require.Len(t, receivedOpts.Models, 2)
	require.Equal(t, "model-a", receivedOpts.Models[0])
	require.Equal(t, "model-b", receivedOpts.Models[1], "receivedOpts.Models = %v, want [model-a model-b]", receivedOpts.Models)
}

func TestProviderFactory_Create_PassesInstanceName(t *testing.T) {
	factory := NewProviderFactory()

	var receivedOpts ProviderOptions
	factory.Add(Registration{
		Type: "test",
		New: func(cfg ProviderConfig, opts ProviderOptions) core.Provider {
			receivedOpts = opts
			return &factoryMockProvider{}
		},
	})
	_, err := factory.Create(ProviderConfig{Name: "test-eu", Type: "test"})
	require.NoError(t, err)
	require.Equal(t, "test-eu", receivedOpts.Name)
	got := receivedOpts.ClientName("test")
	require.Equal(t, "test-eu", got)
	_, err = factory.Create(ProviderConfig{Name: "  test-us  ", Type: "test"})
	require.NoError(t, err)
	require.Equal(t, "test-us", receivedOpts.Name)
	got = (ProviderOptions{}).ClientName("test")
	require.Equal(t, "test", got)
}
