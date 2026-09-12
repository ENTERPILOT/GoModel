package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registryMockProvider is a mock implementation of core.Provider for Registry testing.
// It includes all fields needed for testing the full registry lifecycle.
type registryMockProvider struct {
	name              string
	chatResponse      *core.ChatResponse
	responsesResponse *core.ResponsesResponse
	modelsResponse    *core.ModelsResponse
	err               error
	listModelsDelay   time.Duration
	listModelsStarted chan struct{}
	listModelsBlocked chan struct{}
	listModelsRelease chan struct{}
}

func (m *registryMockProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.chatResponse, nil
}

func (m *registryMockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(nil), nil
}

func (m *registryMockProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if m.listModelsStarted != nil {
		select {
		case m.listModelsStarted <- struct{}{}:
		default:
		}
	}
	if m.listModelsDelay > 0 {
		select {
		case <-time.After(m.listModelsDelay):
		case <-ctx.Done():
			if m.listModelsBlocked != nil {
				select {
				case m.listModelsBlocked <- struct{}{}:
				default:
				}
			}
			if m.listModelsRelease != nil {
				<-m.listModelsRelease
			}
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.modelsResponse, nil
}

func (m *registryMockProvider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.responsesResponse, nil
}

func (m *registryMockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(nil), nil
}

func (m *registryMockProvider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("not supported", nil)
}

func TestModelRegistry(t *testing.T) {
	t.Run("RegisterProvider", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)

		assert.Equal(t, 1, registry.ProviderCount())
	})

	t.Run("Initialize", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model-1", Object: "model", OwnedBy: "test"},
					{ID: "test-model-2", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)

		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 2, registry.ModelCount())
	})

	t.Run("ConfiguredModelsFallbackModeKeepsUpstreamWhenAvailable", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "configured-model", Object: "model", OwnedBy: "upstream"},
					{ID: "upstream-extra", Object: "model", OwnedBy: "upstream"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")
		registry.SetProviderConfiguredModels("test", []string{"configured-model"})

		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, registry.ModelCount())
		require.True(t, registry.Supports("upstream-extra"))
	})

	t.Run("ConfiguredModelsFallbackModeUsesConfiguredWhenUpstreamFails", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			err:  errors.New("models unavailable"),
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")
		registry.SetProviderConfiguredModels("test", []string{" configured-model ", "configured-model", "fallback-only"})

		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		require.Equal(t, 2, registry.ModelCount())
		require.True(t, registry.Supports("configured-model"))
		require.True(t, registry.Supports("fallback-only"), "expected configured fallback models to be registered, got %+v", registry.ListModels())

		model := registry.GetModel("configured-model")
		require.NotNil(t, model)
		require.Equal(t, "model", model.Model.Object)
		require.Equal(t, "test", model.Model.OwnedBy)
		require.Greater(t, model.Model.Created, int64(0))

		snapshots := registry.ProviderRuntimeSnapshots()
		require.Len(t, snapshots, 1)
		require.Contains(t, snapshots[0].LastModelFetchError, "models unavailable")
		require.Nil(t, snapshots[0].LastModelFetchSuccessAt)
	})

	t.Run("SuccessfulLiveModelFetchClearsAvailabilityError", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "ollama",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "qwen3:8b", Object: "model", OwnedBy: "ollama"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "ollama", "ollama")
		registry.RecordAvailabilityCheck("ollama", errors.New("connection refused"))
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		snapshots := registry.ProviderRuntimeSnapshots()
		require.Len(t, snapshots, 1)
		require.Empty(t, snapshots[0].LastAvailabilityError)
		require.NotNil(t, snapshots[0].LastAvailabilityOKAt)
	})

	t.Run("TargetedRefreshWithEmptyInventoryClearsStaleProviderModels", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "ollama",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "qwen3:8b", Object: "model", OwnedBy: "ollama"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "ollama", "ollama")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		require.True(t, registry.Supports("ollama/qwen3:8b"))

		mock.modelsResponse = &core.ModelsResponse{Object: "list", Data: []core.Model{}}
		_, err = registry.RefreshProviderModels(context.Background(), "ollama")
		require.Error(t, err)
		require.Contains(t, err.Error(), "provider returned no models")
		require.False(t, registry.Supports("ollama/qwen3:8b"))
		require.Equal(t, 0, registry.ModelCount())
	})

	t.Run("ConfiguredModelsAllowlistModeSkipsUpstreamAndUsesConfiguredModels", func(t *testing.T) {
		registry := NewModelRegistry()
		registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeAllowlist)
		var listCount atomic.Int32
		mock := &countingRegistryMockProvider{
			listCount: &listCount,
			registryMockProvider: &registryMockProvider{
				name: "test",
				modelsResponse: &core.ModelsResponse{
					Object: "list",
					Data: []core.Model{
						{ID: "configured-model", Object: "model", OwnedBy: "upstream", Created: 123},
						{ID: "upstream-extra", Object: "model", OwnedBy: "upstream", Created: 456},
					},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test-type")
		registry.SetProviderConfiguredModels("test", []string{"missing-configured", "configured-model"})

		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		require.Equal(t, int32(0), listCount.Load())
		require.Equal(t, 2, registry.ModelCount())
		require.False(t, registry.Supports("upstream-extra"))

		configured := registry.GetModel("configured-model")
		require.NotNil(t, configured)
		require.Greater(t, configured.Model.Created, int64(0))
		require.Equal(t, "test-type", configured.Model.OwnedBy)

		snapshots := registry.ProviderRuntimeSnapshots()
		require.Len(t, snapshots, 1)
		require.NotNil(t, snapshots[0].LastModelFetchSuccessAt)
		require.NotEqual(t, 0, snapshots[0].DiscoveredModelCount)
		require.False(t, snapshots[0].UsingCachedModels)

		missing := registry.GetModel("missing-configured")
		require.NotNil(t, missing)
		require.Equal(t, "test-type", missing.Model.OwnedBy)
	})

	t.Run("ConfiguredModelsAllowlistModeUsesUpstreamWhenNoConfiguredModels", func(t *testing.T) {
		registry := NewModelRegistry()
		registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeAllowlist)
		var listCount atomic.Int32
		mock := &countingRegistryMockProvider{
			listCount: &listCount,
			registryMockProvider: &registryMockProvider{
				name: "test",
				modelsResponse: &core.ModelsResponse{
					Object: "list",
					Data: []core.Model{
						{ID: "upstream-model", Object: "model", OwnedBy: "upstream"},
					},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test-type")

		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		require.Equal(t, int32(1), listCount.Load())
		require.True(t, registry.Supports("upstream-model"))

		snapshots := registry.ProviderRuntimeSnapshots()
		require.Len(t, snapshots, 1)
		require.NotNil(t, snapshots[0].LastModelFetchSuccessAt)
	})

	t.Run("GetProvider", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)
		_ = registry.Initialize(context.Background())

		provider := registry.GetProvider("test-model")
		assert.Equal(t, mock, provider)

		provider = registry.GetProvider("unknown-model")
		assert.Nil(t, provider)
	})

	t.Run("Supports", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)
		_ = registry.Initialize(context.Background())

		assert.True(t, registry.Supports("test-model"))
		assert.False(t, registry.Supports("unknown-model"))
	})

	t.Run("ProviderOwnedRawSlashModel", func(t *testing.T) {
		registry := NewModelRegistry()
		openRouter := &registryMockProvider{
			name: "openrouter",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "openrouter/free", Object: "model", OwnedBy: "openrouter"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
		_ = registry.Initialize(context.Background())

		require.True(t, registry.Supports("openrouter/free"))
		provider := registry.GetProvider("openrouter/free")
		require.Equal(t, openRouter, provider)

		model, ok := registry.LookupModel("openrouter/free")
		require.True(t, ok)
		require.NotNil(t, model)
		require.Equal(t, "openrouter/free", model.ID)
		got := registry.GetProviderType("openrouter/free")
		require.Equal(t, "openrouter", got)
		got = registry.GetProviderName("openrouter/free")
		require.Equal(t, "openrouter", got)
	})

	t.Run("GetModel", func(t *testing.T) {
		registry := NewModelRegistry()
		expectedModel := core.Model{
			ID:      "test-model",
			Object:  "model",
			OwnedBy: "test-provider",
			Created: 1234567890,
		}
		mock := &registryMockProvider{
			name: "test-provider",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{expectedModel},
			},
		}
		registry.RegisterProvider(mock)
		_ = registry.Initialize(context.Background())

		modelInfo := registry.GetModel("test-model")
		require.NotNil(t, modelInfo)
		assert.Equal(t, expectedModel.ID, modelInfo.Model.ID)
		assert.Equal(t, expectedModel.OwnedBy, modelInfo.Model.OwnedBy)
		assert.Equal(t, expectedModel.Created, modelInfo.Model.Created)
		assert.Equal(t, mock, modelInfo.Provider)

		unknownInfo := registry.GetModel("unknown-model")
		assert.Nil(t, unknownInfo)
	})

	t.Run("EnrichModelsReplacesPublishedModelInfo", func(t *testing.T) {
		registry := NewModelRegistry()

		mock := &registryMockProvider{
			name: "test-provider",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{
						ID:      "test-model",
						Object:  "model",
						OwnedBy: "test-provider",
					},
				},
			},
		}
		registry.RegisterProviderWithType(mock, "openai")
		_ = registry.Initialize(context.Background())

		before := registry.GetModel("test-model")
		require.NotNil(t, before)
		require.Nil(t, before.Model.Metadata)

		raw := []byte(`{
			"version": 1,
			"updated_at": "2025-01-01T00:00:00Z",
			"providers": {
				"openai": {
					"display_name": "OpenAI",
					"api_type": "openai",
					"supported_modes": ["chat"]
				}
			},
			"models": {
				"test-model": {
					"display_name": "Test Model",
					"modes": ["chat"]
				}
			},
			"provider_models": {}
		}`)
		list, err := modeldata.Parse(raw)
		require.NoError(t, err)

		registry.SetModelList(list, raw)
		registry.EnrichModels()

		require.Nil(t, before.Model.Metadata)

		after := registry.GetModel("test-model")
		require.NotNil(t, after)
		require.NotSame(t, before, after)
		require.NotNil(t, after.Model.Metadata)
		require.Equal(t, "Test Model", after.Model.Metadata.DisplayName)

		lookup, ok := registry.LookupModel("test-model")
		require.True(t, ok)
		require.NotNil(t, lookup)
		require.NotNil(t, lookup.Metadata)
		require.Equal(t, "Test Model", lookup.Metadata.DisplayName)
	})

	t.Run("EnrichModelsUsesAliasesWithoutAddingSyntheticModels", func(t *testing.T) {
		registry := NewModelRegistry()

		mock := &registryMockProvider{
			name: "gemini-provider",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{
						ID:      "claude-opus-4",
						Object:  "model",
						OwnedBy: "gemini",
					},
				},
			},
		}
		registry.RegisterProviderWithType(mock, "gemini")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		raw := []byte(`{
			"version": 1,
			"updated_at": "2025-01-01T00:00:00Z",
			"providers": {
				"gemini": {
					"display_name": "Gemini",
					"api_type": "openai",
					"supported_modes": ["chat"]
				}
			},
			"models": {
				"claude-4-opus": {
					"display_name": "Claude 4 Opus",
					"modes": ["chat"],
					"aliases": ["claude-opus-4", "gemini/claude-opus-4"]
				}
			},
			"provider_models": {
				"gemini/claude-4-opus": {
					"model_ref": "claude-4-opus",
					"enabled": true,
					"context_window": 200000
				}
			}
		}`)
		list, err := modeldata.Parse(raw)
		require.NoError(t, err)

		registry.SetModelList(list, raw)
		registry.EnrichModels()

		require.Equal(t, 1, registry.ModelCount())
		synthetic := registry.GetModel("claude-4-opus")
		require.Nil(t, synthetic)

		info := registry.GetModel("claude-opus-4")
		require.NotNil(t, info)
		require.Equal(t, "claude-opus-4", info.Model.ID)
		require.NotNil(t, info.Model.Metadata)
		require.Equal(t, "Claude 4 Opus", info.Model.Metadata.DisplayName)
		require.NotNil(t, info.Model.Metadata.ContextWindow)
		require.Equal(t, 200000, *info.Model.Metadata.ContextWindow)
	})

	t.Run("RefreshModelListDownloadsAndEnrichesCurrentModels", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "openai-provider",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{
						ID:      "gpt-test",
						Object:  "model",
						OwnedBy: "openai",
					},
				},
			},
		}
		registry.RegisterProviderWithType(mock, "openai")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"version": 1,
				"updated_at": "2026-04-11T00:00:00Z",
				"providers": {
					"openai": {
						"display_name": "OpenAI",
						"api_type": "openai",
						"supported_modes": ["chat"]
					}
				},
				"models": {
					"gpt-test": {
						"display_name": "GPT Test",
						"modes": ["chat"],
						"capabilities": {"tool_calling": true}
					}
				},
				"provider_models": {}
			}`))
		}))
		defer server.Close()

		count, err := registry.RefreshModelList(context.Background(), server.URL)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		info := registry.GetModel("gpt-test")
		require.NotNil(t, info)
		require.NotNil(t, info.Model.Metadata)
		require.Equal(t, "GPT Test", info.Model.Metadata.DisplayName)
		require.True(t, info.Model.Metadata.Capabilities["tool_calling"])
	})

	t.Run("InitializeReturnsGatewayErrorWhenContextCanceledBeforeAcquire", func(t *testing.T) {
		registry := NewModelRegistry()
		ch := registry.refreshSemaphore()
		ch <- struct{}{}
		defer func() { <-ch }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := registry.Initialize(ctx)
		require.Error(t, err)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		require.Equal(t, http.StatusRequestTimeout, gatewayErr.HTTPStatusCode())
		require.Equal(t, "model_registry", gatewayErr.Provider)
	})

	t.Run("RefreshModelListReturnsGatewayErrorWhenContextCanceledBeforeAcquire", func(t *testing.T) {
		registry := NewModelRegistry()
		ch := registry.refreshSemaphore()
		ch <- struct{}{}
		defer func() { <-ch }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := registry.RefreshModelList(ctx, "https://example.test/models.min.json")
		require.Error(t, err)

		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		require.Equal(t, http.StatusRequestTimeout, gatewayErr.HTTPStatusCode())
		require.Equal(t, "model_registry", gatewayErr.Provider)
	})

	t.Run("DuplicateModels", func(t *testing.T) {
		registry := NewModelRegistry()
		mock1 := &registryMockProvider{
			name: "provider1",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "shared-model", Object: "model", OwnedBy: "provider1"},
				},
			},
		}
		mock2 := &registryMockProvider{
			name: "provider2",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "shared-model", Object: "model", OwnedBy: "provider2"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock1, "provider1", "openai")
		registry.RegisterProviderWithNameAndType(mock2, "provider2", "openai")
		_ = registry.Initialize(context.Background())

		assert.Equal(t, 1, registry.ModelCount())

		provider := registry.GetProvider("shared-model")
		assert.Equal(t, mock1, provider)
		provider = registry.GetProvider("provider2/shared-model")
		assert.Equal(t, mock2, provider)
	})

	t.Run("SlashModelFallsBackToRawModelWhenPrefixIsNotConfiguredProvider", func(t *testing.T) {
		registry := NewModelRegistry()
		openRouter := &registryMockProvider{
			name:         "openrouter",
			chatResponse: &core.ChatResponse{ID: "openrouter"},
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "google/gemini-xyz", Object: "model", OwnedBy: "openrouter"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		router, err := NewRouter(registry)
		require.NoError(t, err)

		resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "google/gemini-xyz"})
		require.NoError(t, err)
		require.Equal(t, "openrouter", resp.ID)
	})

	t.Run("SlashModelDoesNotFallBackToRawModelWhenPrefixIsConfiguredProvider", func(t *testing.T) {
		registry := NewModelRegistry()
		google := &registryMockProvider{
			name:         "google",
			chatResponse: &core.ChatResponse{ID: "google"},
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "gemini-1.5-pro", Object: "model", OwnedBy: "google"},
				},
			},
		}
		openRouter := &registryMockProvider{
			name:         "openrouter",
			chatResponse: &core.ChatResponse{ID: "openrouter"},
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "google/gemini-xyz", Object: "model", OwnedBy: "openrouter"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(google, "google", "gemini")
		registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		router, err := NewRouter(registry)
		require.NoError(t, err)

		_, err = router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "google/gemini-xyz"})
		require.Error(t, err)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusNotFound, gwErr.HTTPStatusCode())
	})

	t.Run("AllProvidersFail", func(t *testing.T) {
		registry := NewModelRegistry()
		mock1 := &registryMockProvider{
			name: "provider1",
			err:  errors.New("provider1 error"),
		}
		mock2 := &registryMockProvider{
			name: "provider2",
			err:  errors.New("provider2 error"),
		}
		registry.RegisterProvider(mock1)
		registry.RegisterProvider(mock2)

		err := registry.Initialize(context.Background())
		require.EqualError(t, err, "failed to fetch models from any provider")
	})

	t.Run("FailedRefreshRecordsRuntimeErrorAndKeepsInventory", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		mock.err = errors.New("refresh error")
		err = registry.Initialize(context.Background())
		require.Error(t, err)

		snapshots := registry.ProviderRuntimeSnapshots()
		require.Len(t, snapshots, 1)

		snapshot := snapshots[0]
		require.Equal(t, 1, snapshot.DiscoveredModelCount)
		require.Contains(t, snapshot.LastModelFetchError, "refresh error")
		require.NotNil(t, snapshot.LastModelFetchAt)
	})

	t.Run("EmptyRefreshRecordsRuntimeErrorAndKeepsInventory", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithNameAndType(mock, "test", "test")
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		mock.modelsResponse = &core.ModelsResponse{Object: "list"}
		err = registry.Initialize(context.Background())
		require.Error(t, err)

		snapshots := registry.ProviderRuntimeSnapshots()
		require.Len(t, snapshots, 1)

		snapshot := snapshots[0]
		require.Equal(t, 1, snapshot.DiscoveredModelCount)
		require.Contains(t, snapshot.LastModelFetchError, "empty model list")
		require.NotNil(t, snapshot.LastModelFetchAt)
	})

	t.Run("ListModelsOrdering", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "zebra-model", Object: "model", OwnedBy: "test"},
					{ID: "alpha-model", Object: "model", OwnedBy: "test"},
					{ID: "middle-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)
		_ = registry.Initialize(context.Background())

		for range 5 {
			models := registry.ListModels()
			require.Len(t, models, 3)
			assert.Equal(t, "alpha-model", models[0].ID)
			assert.Equal(t, "middle-model", models[1].ID)
			assert.Equal(t, "zebra-model", models[2].ID)
		}
	})

	t.Run("RefreshDoesNotBlockReads", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProvider(mock)
		_ = registry.Initialize(context.Background())

		require.True(t, registry.Supports("test-model"))

		err := registry.Refresh(context.Background())
		require.NoError(t, err)
		assert.True(t, registry.Supports("test-model"))
	})

	t.Run("GetProviderType", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}
		registry.RegisterProviderWithType(mock, "openai")
		_ = registry.Initialize(context.Background())

		pType := registry.GetProviderType("test-model")
		assert.Equal(t, "openai", pType)

		pType = registry.GetProviderType("unknown-model")
		assert.Empty(t, pType)
	})
}

// A provider whose refresh fails keeps serving its previous inventory, marked
// stale: models stay resolvable for direct requests, ModelAvailable reports
// false so load balancing skips them, and the next successful refresh clears
// the flag.
func TestInitialize_FailedRefreshKeepsPreviousInventoryAsStale(t *testing.T) {
	registry := NewModelRegistry()
	flaky := &registryMockProvider{
		name: "flaky",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "flaky-model", Object: "model", OwnedBy: "flaky"}},
		},
	}
	steady := &registryMockProvider{
		name: "steady",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "steady-model", Object: "model", OwnedBy: "steady"}},
		},
	}
	registry.RegisterProviderWithNameAndType(flaky, "flaky", "flaky")
	registry.RegisterProviderWithNameAndType(steady, "steady", "steady")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	require.True(t, registry.ModelAvailable("flaky/flaky-model"))

	flaky.err = errors.New("connection refused")
	err = registry.Initialize(context.Background())
	require.NoError(t, err)
	require.Equal(t, registry.GetProvider("flaky/flaky-model"), flaky)
	require.True(t, registry.Supports("flaky/flaky-model"))
	require.False(t, registry.ModelAvailable("flaky/flaky-model"))
	require.True(t, registry.ModelAvailable("steady/steady-model"))

	var flakySnapshot ProviderRuntimeSnapshot
	for _, snapshot := range registry.ProviderRuntimeSnapshots() {
		if snapshot.Name == "flaky" {
			flakySnapshot = snapshot
		}
	}
	require.True(t, flakySnapshot.InventoryStale)
	require.NotEqual(t, 0, flakySnapshot.DiscoveredModelCount)
	require.NotEmpty(t, flakySnapshot.LastModelFetchError)

	flaky.err = nil
	err = registry.Initialize(context.Background())
	require.NoError(t, err)
	require.True(t, registry.ModelAvailable("flaky/flaky-model"))
}

// When several providers serve the same bare model ID, a stale provider loses
// the unqualified slot to a healthy duplicate — the same routing the old
// inventory wipe produced — while staying reachable via its qualified name.
func TestInitialize_StaleProviderLosesBareModelIDToHealthyDuplicate(t *testing.T) {
	registry := NewModelRegistry()
	sharedModels := func(owner string) *core.ModelsResponse {
		return &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "shared-model", Object: "model", OwnedBy: owner}},
		}
	}
	first := &registryMockProvider{name: "first", modelsResponse: sharedModels("first")}
	second := &registryMockProvider{name: "second", modelsResponse: sharedModels("second")}
	registry.RegisterProviderWithNameAndType(first, "first", "first")
	registry.RegisterProviderWithNameAndType(second, "second", "second")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	require.Equal(t, registry.GetProvider("shared-model"), first)

	first.err = errors.New("connection refused")
	err = registry.Initialize(context.Background())
	require.NoError(t, err)
	require.Equal(t, registry.GetProvider("shared-model"), second)
	require.Equal(t, registry.GetProvider("first/shared-model"), first)
	require.True(t, registry.ModelAvailable("shared-model"))
}

// A provider that goes offline must disappear from every model listing
// (GET /v1/models, dashboard model list, category counts) while its
// carried-forward inventory stays resolvable for direct requests, and it must
// reappear once the provider recovers (issue #705).
func TestStaleProviderModelsAreNotAdvertised(t *testing.T) {
	registry, _, beta := registerTwoProviderRegistry(t)

	listedIDs := func(t *testing.T) map[string]bool {
		t.Helper()
		ids := make(map[string]bool)
		for _, model := range registry.ListPublicModels() {
			ids["public:"+model.ID] = true
		}
		for _, entry := range registry.ListModelsWithProvider() {
			ids["provider:"+entry.Selector] = true
		}
		for _, model := range registry.ListModels() {
			ids["bare:"+model.ID] = true
		}
		return ids
	}

	categorySelectors := func(t *testing.T) map[string]bool {
		t.Helper()
		selectors := make(map[string]bool)
		for _, entry := range registry.ListModelsWithProviderByCategory(core.CategoryEmbedding) {
			selectors[entry.Selector] = true
		}
		return selectors
	}

	before := listedIDs(t)
	for _, key := range []string{"public:beta/beta-model", "provider:beta/beta-model", "bare:beta-model"} {
		require.True(t, before[key], "%s missing from listings while beta is healthy", key)
	}
	require.True(t, categorySelectors(t)["beta/beta-model"])

	beta.err = errors.New("connection refused")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	after := listedIDs(t)
	for _, key := range []string{"public:beta/beta-model", "provider:beta/beta-model", "bare:beta-model"} {
		assert.False(t, after[key], "%s still advertised after beta went offline", key)
	}
	for _, key := range []string{"public:alpha/alpha-model", "provider:alpha/alpha-model", "bare:alpha-model"} {
		assert.True(t, after[key], "%s missing from listings, want healthy provider unaffected", key)
	}
	for _, counts := range registry.GetCategoryCounts() {
		if counts.Category == core.CategoryAll {
			assert.Equal(t, 1, counts.Count, "GetCategoryCounts()[all] with beta offline")
		}
	}
	afterCategory := categorySelectors(t)
	assert.False(t, afterCategory["beta/beta-model"], "beta/beta-model still in embedding category after beta went offline")
	assert.True(t, afterCategory["alpha/alpha-model"])

	// Direct requests must still resolve the carried inventory (honest 502 at
	// the provider instead of "model not found").
	assert.True(t, registry.Supports("beta/beta-model"))

	beta.err = nil
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	recovered := listedIDs(t)
	for _, key := range []string{"public:beta/beta-model", "provider:beta/beta-model", "bare:beta-model"} {
		assert.True(t, recovered[key], "%s missing from listings after recovery, want advertised again", key)
	}
}

// The fast recheck loop re-probes only providers whose latest refresh failed,
// so a recovered provider is picked up within the recheck interval instead of
// waiting for the next full refresh.
func TestStartBackgroundRefresh_RechecksFailedProviders(t *testing.T) {
	registry := NewModelRegistry()
	flaky := &registryMockProvider{
		name: "flaky",
		err:  errors.New("connection refused"),
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "flaky-model", Object: "model", OwnedBy: "flaky"}},
		},
	}
	registry.RegisterProviderWithNameAndType(flaky, "flaky", "flaky")

	// Mark the provider failed while nothing runs concurrently.
	require.Error(t, registry.Initialize(context.Background()))
	got := registry.FailedProviderNames()
	require.Len(t, got, 1)
	require.Equal(t, "flaky", got[0])

	// The provider recovers before the loop starts (avoids racing the mock).
	flaky.err = nil

	// Full refresh is an hour away; only the recheck loop can discover the
	// recovery.
	stop := registry.StartBackgroundRefresh(time.Hour, 10*time.Millisecond, "")
	defer stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if registry.ModelAvailable("flaky/flaky-model") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, registry.ModelAvailable("flaky/flaky-model"))
	got = registry.FailedProviderNames()
	require.Empty(t, got)
}

// registerTwoProviderRegistry seeds a registry with two healthy providers and
// runs the initial discovery sweep.
func registerTwoProviderRegistry(t *testing.T) (*ModelRegistry, *registryMockProvider, *registryMockProvider) {
	t.Helper()
	registry := NewModelRegistry()
	singleModel := func(owner string) *core.ModelsResponse {
		return &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{{
				ID: owner + "-model", Object: "model", OwnedBy: owner,
				Metadata: &core.ModelMetadata{
					Modes:      []string{"embedding"},
					Categories: []core.ModelCategory{core.CategoryEmbedding},
				},
			}},
		}
	}
	alpha := &registryMockProvider{name: "alpha", modelsResponse: singleModel("alpha")}
	beta := &registryMockProvider{name: "beta", modelsResponse: singleModel("beta")}
	registry.RegisterProviderWithNameAndType(alpha, "alpha", "alpha")
	registry.RegisterProviderWithNameAndType(beta, "beta", "beta")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	return registry, alpha, beta
}

// A sweep in which every provider fails must keep the previous inventory
// routable: with no healthy alternative, marking everything stale would only
// turn provider-level 502/503s into alias 404s (and would break aliased
// traffic on control-plane-only outages).
func TestInitialize_TotalRefreshFailureKeepsRouting(t *testing.T) {
	registry, alpha, beta := registerTwoProviderRegistry(t)

	alpha.err = errors.New("connection refused")
	beta.err = errors.New("connection refused")
	require.Error(t, registry.Initialize(context.Background()))

	for _, model := range []string{"alpha/alpha-model", "beta/beta-model"} {
		require.True(t, registry.ModelAvailable(model), "ModelAvailable(%q) = false after total refresh failure, want true (no healthy alternative to route to)", model)
	}
}

// A failed per-provider probe (the recheck loop, request-time refresh) marks
// the provider stale as soon as a healthy alternative exists, instead of
// waiting for the next full sweep.
func TestRefreshProviderModels_FailureMarksStaleWhenAlternativeHealthy(t *testing.T) {
	registry, _, beta := registerTwoProviderRegistry(t)

	beta.err = errors.New("connection refused")
	_, err := registry.RefreshProviderModels(context.Background(), "beta")
	require.Error(t, err)
	require.False(t, registry.ModelAvailable("beta/beta-model"))
	require.True(t, registry.Supports("beta/beta-model"))
	require.True(t, registry.ModelAvailable("alpha/alpha-model"))
}

// After a total outage, a recovering provider must retire its still-down peer
// from load balancing at the next probe — not at the next full sweep.
func TestRefreshProviderModels_TotalOutageRecoveryRetiresStillDownPeer(t *testing.T) {
	registry, alpha, beta := registerTwoProviderRegistry(t)

	alpha.err = errors.New("connection refused")
	beta.err = errors.New("connection refused")
	require.Error(t, registry.Initialize(context.Background()))
	_, err := // While nothing is healthy, a failed probe must not retire the provider.
		registry.RefreshProviderModels(context.Background(), "beta")
	require.Error(t, err)
	require.True(t, registry.ModelAvailable("beta/beta-model"))

	// Alpha recovers; the next failed probe of beta retires it.
	alpha.err = nil
	_, err = registry.RefreshProviderModels(context.Background(), "alpha")
	require.NoError(t, err)
	_, err = registry.RefreshProviderModels(context.Background(), "beta")
	require.Error(t, err)
	require.False(t, registry.ModelAvailable("beta/beta-model"))
	require.True(t, registry.ModelAvailable("alpha/alpha-model"))
}

// availabilityFailingProvider wraps the registry mock with a failing
// CheckAvailability so the availability-gate path can be exercised.
type availabilityFailingProvider struct {
	*registryMockProvider
	availabilityErr error
}

func (p *availabilityFailingProvider) CheckAvailability(context.Context) error {
	return p.availabilityErr
}

// A failed availability check during a per-provider refresh marks the
// provider stale just like a failed model fetch.
func TestRefreshProviderModels_AvailabilityFailureMarksStale(t *testing.T) {
	registry := NewModelRegistry()
	alpha := &registryMockProvider{
		name: "alpha",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "alpha-model", Object: "model", OwnedBy: "alpha"}},
		},
	}
	beta := &availabilityFailingProvider{
		registryMockProvider: &registryMockProvider{
			name: "beta",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "beta-model", Object: "model", OwnedBy: "beta"}},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(alpha, "alpha", "alpha")
	registry.RegisterProviderWithNameAndType(beta, "beta", "beta")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	beta.availabilityErr = errors.New("connection refused")
	_, err = registry.RefreshProviderModels(context.Background(), "beta")
	require.Error(t, err)
	require.False(t, registry.ModelAvailable("beta/beta-model"))
	got := // The availability failure never set a model fetch error, but the recheck
		// loop must still re-probe the provider or it would stay stale until the
		// next full sweep.
		registry.FailedProviderNames()
	require.Len(t, got, 1)
	require.Equal(t, "beta", got[0])

	// Recovery through the recheck path restores availability.
	beta.availabilityErr = nil
	_, err = registry.RefreshProviderModels(context.Background(), "beta")
	require.NoError(t, err)
	require.True(t, registry.ModelAvailable("beta/beta-model"))
	got = registry.FailedProviderNames()
	require.Empty(t, got)
}

// A provider with a failed availability probe does not count as the healthy
// alternative that justifies retiring another provider from load balancing.
func TestRefreshProviderModels_AvailabilityFailingPeerIsNotHealthyAlternative(t *testing.T) {
	registry, _, beta := registerTwoProviderRegistry(t)

	registry.RecordAvailabilityCheck("alpha", errors.New("connection refused"))

	beta.err = errors.New("connection refused")
	_, err := registry.RefreshProviderModels(context.Background(), "beta")
	require.Error(t, err)
	require.True(t, registry.ModelAvailable("beta/beta-model"))
}

// The refresh sweep shares one context budget across all providers; a slow
// upstream must not starve the providers registered after it out of that
// budget (a starved provider is recorded as failed and its inventory goes
// stale — or, on first fetch, is never discovered at all).
func TestInitialize_SlowProviderDoesNotStarveOthers(t *testing.T) {
	registry := NewModelRegistry()
	slow := &registryMockProvider{
		name:            "slow",
		listModelsDelay: 5 * time.Second,
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "slow-model", Object: "model", OwnedBy: "slow"}},
		},
	}
	// A small delay makes the fast mock honor context cancellation the way a
	// real HTTP call would, so sequential starvation is actually observable.
	fast := &registryMockProvider{
		name:            "fast",
		listModelsDelay: time.Millisecond,
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "fast-model", Object: "model", OwnedBy: "fast"}},
		},
	}
	registry.RegisterProviderWithNameAndType(slow, "slow", "slow")
	registry.RegisterProviderWithNameAndType(fast, "fast", "fast")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := registry.Initialize(ctx)
	require.NoError(t, err)
	provider := registry.GetProvider("fast-model")
	require.Equal(t, fast, provider)
	provider = registry.GetProvider("slow-model")
	require.Nil(t, provider)
}

func TestInitialize_LogsSingleMetadataSummaryPerCycle(t *testing.T) {
	registry := NewModelRegistry()

	openAIProvider := &registryMockProvider{
		name: "openai-primary",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-test", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	anthropicProvider := &registryMockProvider{
		name: "anthropic-primary",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "claude-test", Object: "model", OwnedBy: "anthropic"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(openAIProvider, "openai-primary", "openai")
	registry.RegisterProviderWithNameAndType(anthropicProvider, "anthropic-primary", "anthropic")

	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {
			"openai": {
				"display_name": "OpenAI",
				"api_type": "openai",
				"supported_modes": ["chat"]
			},
			"anthropic": {
				"display_name": "Anthropic",
				"api_type": "openai",
				"supported_modes": ["chat"]
			}
		},
		"models": {
			"gpt-test": {
				"display_name": "GPT Test",
				"modes": ["chat"]
			},
			"claude-test": {
				"display_name": "Claude Test",
				"modes": ["chat"]
			}
		},
		"provider_models": {}
	}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)

	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	logs := buf.String()
	got := strings.Count(logs, `"msg":"enriched models with metadata"`)
	require.Equal(t, 0, got)
	got = strings.Count(logs, `"msg":"model registry initialized"`)
	require.Equal(t, 1, got)
	require.Contains(t, logs, `"metadata_enriched":2`)
	require.Contains(t, logs, `"metadata_total":2`)
	require.Contains(t, logs, `"metadata_providers":2`)
}

func TestListModelsWithProvider_Empty(t *testing.T) {
	registry := NewModelRegistry()
	models := registry.ListModelsWithProvider()
	assert.Empty(t, models)
}

func TestListModelsWithProvider_Sorted(t *testing.T) {
	registry := NewModelRegistry()

	mock1 := &registryMockProvider{
		name: "provider1",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "zebra-model", Object: "model", OwnedBy: "provider1"},
				{ID: "alpha-model", Object: "model", OwnedBy: "provider1"},
			},
		},
	}
	mock2 := &registryMockProvider{
		name: "provider2",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "middle-model", Object: "model", OwnedBy: "provider2"},
			},
		},
	}
	registry.RegisterProviderWithType(mock1, "openai")
	registry.RegisterProviderWithType(mock2, "anthropic")
	_ = registry.Initialize(context.Background())

	models := registry.ListModelsWithProvider()
	require.Len(t, models, 3)
	assert.Equal(t, "middle-model", models[0].Model.ID)
	assert.Equal(t, "alpha-model", models[1].Model.ID)
	assert.Equal(t, "zebra-model", models[2].Model.ID)
}

func TestListModelsWithProvider_IncludesProviderType(t *testing.T) {
	registry := NewModelRegistry()

	mock1 := &registryMockProvider{
		name: "provider1",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	mock2 := &registryMockProvider{
		name: "provider2",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "claude-3", Object: "model", OwnedBy: "anthropic"},
			},
		},
	}
	registry.RegisterProviderWithType(mock1, "openai")
	registry.RegisterProviderWithType(mock2, "anthropic")
	_ = registry.Initialize(context.Background())

	models := registry.ListModelsWithProvider()
	require.Len(t, models, 2)

	// Models are sorted: claude-3 before gpt-4
	assert.Equal(t, "anthropic", models[0].ProviderType)
	assert.Equal(t, "openai", models[1].ProviderType)
}

func TestInitialize_EnrichesAllProviderSpecificModels(t *testing.T) {
	registry := NewModelRegistry()

	openAI := &registryMockProvider{
		name: "provider-openai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "shared-model", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	openRouter := &registryMockProvider{
		name: "provider-openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "shared-model", Object: "model", OwnedBy: "openrouter"},
			},
		},
	}

	registry.RegisterProviderWithNameAndType(openAI, "openai-main", "openai")
	registry.RegisterProviderWithNameAndType(openRouter, "openrouter-main", "openrouter")

	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {
			"openai": {"display_name": "OpenAI", "api_type": "openai", "supported_modes": ["chat"]},
			"openrouter": {"display_name": "OpenRouter", "api_type": "openai", "supported_modes": ["chat"]}
		},
		"models": {
			"shared-model": {"display_name": "Shared Model", "modes": ["chat"]}
		},
		"provider_models": {
			"openai/shared-model": {"model_ref": "shared-model", "enabled": true, "context_window": 111111},
			"openrouter/shared-model": {"model_ref": "shared-model", "enabled": true, "context_window": 222222}
		}
	}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	openAIInfo := registry.GetModel("openai-main/shared-model")
	require.NotNil(t, openAIInfo)
	require.NotNil(t, openAIInfo.Model.Metadata)
	require.NotNil(t, openAIInfo.Model.Metadata.ContextWindow)
	require.Equal(t, 111111, *openAIInfo.Model.Metadata.ContextWindow)

	openRouterInfo := registry.GetModel("openrouter-main/shared-model")
	require.NotNil(t, openRouterInfo)
	require.NotNil(t, openRouterInfo.Model.Metadata)
	require.NotNil(t, openRouterInfo.Model.Metadata.ContextWindow)
	require.Equal(t, 222222, *openRouterInfo.Model.Metadata.ContextWindow)
}

func TestListPublicModels_UsesConfiguredProviderNamesAndIncludesDuplicates(t *testing.T) {
	registry := NewModelRegistry()

	openAI := &registryMockProvider{
		name: "provider-openai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	openRouter := &registryMockProvider{
		name: "provider-openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	azure := &registryMockProvider{
		name: "provider-azure",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}

	registry.RegisterProviderWithNameAndType(openAI, "openai", "openai")
	registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
	registry.RegisterProviderWithNameAndType(azure, "azure-openai", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	models := registry.ListPublicModels()
	require.Len(t, models, 3)

	want := []core.Model{
		{ID: "azure-openai/gpt-4o", OwnedBy: "azure-openai"},
		{ID: "openai/gpt-4o", OwnedBy: "openai"},
		{ID: "openrouter/gpt-4o", OwnedBy: "openrouter"},
	}
	for i, model := range want {
		require.Equal(t, model.ID, models[i].ID, "models[%d].ID", i)
		require.Equal(t, model.OwnedBy, models[i].OwnedBy, "models[%d].OwnedBy", i)
	}
}

func TestListModelsWithProvider_UsesConfiguredProviderNamesAndIncludesDuplicates(t *testing.T) {
	registry := NewModelRegistry()

	openAI := &registryMockProvider{
		name: "provider-openai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	openRouter := &registryMockProvider{
		name: "provider-openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
				{ID: "openai/gpt-4o-mini", Object: "model", OwnedBy: "openai"},
			},
		},
	}

	registry.RegisterProviderWithNameAndType(openAI, "openai", "openai")
	registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	models := registry.ListModelsWithProvider()
	require.Len(t, models, 3)

	want := []struct {
		id           string
		providerName string
		providerType string
		selector     string
	}{
		{id: "gpt-4o", providerName: "openai", providerType: "openai", selector: "openai/gpt-4o"},
		{id: "gpt-4o", providerName: "openrouter", providerType: "openrouter", selector: "openrouter/gpt-4o"},
		{id: "openai/gpt-4o-mini", providerName: "openrouter", providerType: "openrouter", selector: "openrouter/openai/gpt-4o-mini"},
	}
	for i, wantModel := range want {
		require.Equal(t, wantModel.id, models[i].Model.ID, "models[%d].Model.ID", i)
		require.Equal(t, wantModel.providerName, models[i].ProviderName, "models[%d].ProviderName", i)
		require.Equal(t, wantModel.providerType, models[i].ProviderType, "models[%d].ProviderType", i)
		require.Equal(t, wantModel.selector, models[i].Selector, "models[%d].Selector", i)
	}
}

// countingRegistryMockProvider wraps registryMockProvider and counts ListModels calls
type countingRegistryMockProvider struct {
	*registryMockProvider
	listCount *atomic.Int32
}

func (c *countingRegistryMockProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	c.listCount.Add(1)
	return c.registryMockProvider.ListModels(ctx)
}

// TestApplyProviderRuntimeUpdates_ClearsStaleErrorOnSuccessfulRefresh locks the
// behavior that a successful refresh (non-zero fetchAt + empty fetch error)
// clears any error left over from a previous failed refresh, regardless of
// whether the success bumps lastModelFetchSuccessAt. This protects against any
// future fetch path that produces a refresh result without touching SuccessAt —
// a stale error must not survive into runtime status.
func TestApplyProviderRuntimeUpdates_ClearsStaleErrorOnSuccessfulRefresh(t *testing.T) {
	registry := NewModelRegistry()

	// Seed runtime state with a prior error.
	registry.providerRuntime["test"] = providerRuntimeState{
		registered:          true,
		lastModelFetchAt:    time.Now().Add(-time.Hour),
		lastModelFetchError: "previous upstream failure",
	}

	// Apply a successful refresh that produced usable models without
	// touching upstream — mimics allowlist mode.
	registry.applyProviderRuntimeUpdatesLocked(map[string]providerRuntimeState{
		"test": {
			registered:       true,
			lastModelFetchAt: time.Now(),
			// lastModelFetchError intentionally empty; SuccessAt deliberately zero.
		},
	})
	got := registry.providerRuntime["test"].lastModelFetchError
	require.Empty(t, got)
}

func TestStartBackgroundRefresh(t *testing.T) {
	t.Run("RefreshesAtInterval", func(t *testing.T) {
		var refreshCount atomic.Int32
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}

		countingMock := &countingRegistryMockProvider{
			registryMockProvider: mock,
			listCount:            &refreshCount,
		}

		registry := NewModelRegistry()
		registry.RegisterProvider(countingMock)
		_ = registry.Initialize(context.Background())

		refreshCount.Store(0)

		interval := 50 * time.Millisecond
		cancel := registry.StartBackgroundRefresh(interval, 0, "")
		defer cancel()

		time.Sleep(interval*3 + 25*time.Millisecond)

		count := refreshCount.Load()
		assert.GreaterOrEqual(t, count, int32(2))
	})

	t.Run("StopsOnCancel", func(t *testing.T) {
		var refreshCount atomic.Int32
		mock := &registryMockProvider{
			name: "test",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "test-model", Object: "model", OwnedBy: "test"},
				},
			},
		}

		countingMock := &countingRegistryMockProvider{
			registryMockProvider: mock,
			listCount:            &refreshCount,
		}

		registry := NewModelRegistry()
		registry.RegisterProvider(countingMock)
		_ = registry.Initialize(context.Background())

		refreshCount.Store(0)

		interval := 50 * time.Millisecond
		cancel := registry.StartBackgroundRefresh(interval, 0, "")
		cancel()

		time.Sleep(interval * 3)

		count := refreshCount.Load()
		assert.LessOrEqual(t, count, int32(1))
	})

	t.Run("CancelWaitsForInFlightRefreshToExit", func(t *testing.T) {
		t.Run("ListModels", func(t *testing.T) {
			var refreshCount atomic.Int32
			mock := &registryMockProvider{
				name: "test",
				modelsResponse: &core.ModelsResponse{
					Object: "list",
					Data: []core.Model{
						{ID: "test-model", Object: "model", OwnedBy: "test"},
					},
				},
			}

			countingMock := &countingRegistryMockProvider{
				registryMockProvider: mock,
				listCount:            &refreshCount,
			}

			registry := NewModelRegistry()
			registry.RegisterProvider(countingMock)
			_ = registry.Initialize(context.Background())
			refreshCount.Store(0)
			mock.listModelsDelay = 5 * time.Second
			mock.listModelsStarted = make(chan struct{}, 1)
			mock.listModelsBlocked = make(chan struct{}, 1)
			mock.listModelsRelease = make(chan struct{})

			cancel := registry.StartBackgroundRefresh(10*time.Millisecond, 0, "")
			select {
			case <-mock.listModelsStarted:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("expected StartBackgroundRefresh to begin ListModels")
			}

			cancelDone := make(chan struct{})
			go func() {
				cancel()
				close(cancelDone)
			}()

			select {
			case <-mock.listModelsBlocked:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("expected ListModels to observe cancellation")
			}

			select {
			case <-cancelDone:
				t.Fatal("cancel() returned before in-flight ListModels finished")
			case <-time.After(50 * time.Millisecond):
			}

			close(mock.listModelsRelease)

			select {
			case <-cancelDone:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("cancel() did not return after releasing ListModels")
			}
		})

		t.Run("ModelListFetch", func(t *testing.T) {
			fetchStarted := make(chan struct{}, 1)
			fetchCanceled := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case fetchStarted <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case fetchCanceled <- struct{}{}:
				default:
				}
			}))
			defer server.Close()

			var refreshCount atomic.Int32
			mock := &registryMockProvider{
				name: "test",
				modelsResponse: &core.ModelsResponse{
					Object: "list",
					Data: []core.Model{
						{ID: "test-model", Object: "model", OwnedBy: "test"},
					},
				},
			}
			countingMock := &countingRegistryMockProvider{
				registryMockProvider: mock,
				listCount:            &refreshCount,
			}

			registry := NewModelRegistry()
			registry.RegisterProvider(countingMock)
			_ = registry.Initialize(context.Background())

			cancel := registry.StartBackgroundRefresh(10*time.Millisecond, 0, server.URL)
			select {
			case <-fetchStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("expected StartBackgroundRefresh to begin model list fetch")
			}

			cancel()

			select {
			case <-fetchCanceled:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("expected model list fetch to be canceled during shutdown")
			}
		})
	})

	t.Run("HandlesRefreshErrors", func(t *testing.T) {
		var refreshCount atomic.Int32
		mock := &registryMockProvider{
			name: "failing",
			err:  errors.New("refresh error"),
		}

		countingMock := &countingRegistryMockProvider{
			registryMockProvider: mock,
			listCount:            &refreshCount,
		}

		registry := NewModelRegistry()
		workingMock := &registryMockProvider{
			name: "working",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data: []core.Model{
					{ID: "working-model", Object: "model", OwnedBy: "working"},
				},
			},
		}
		registry.RegisterProvider(workingMock)
		registry.RegisterProvider(countingMock)
		_ = registry.Initialize(context.Background())

		refreshCount.Store(0)

		interval := 50 * time.Millisecond
		cancel := registry.StartBackgroundRefresh(interval, 0, "")
		defer cancel()

		time.Sleep(interval*3 + 25*time.Millisecond)

		count := refreshCount.Load()
		assert.GreaterOrEqual(t, count, int32(2))
	})
}

func TestListModelsWithProviderByCategory(t *testing.T) {
	registry := NewModelRegistry()
	mock := &registryMockProvider{
		name: "test",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID: "gpt-4o", Object: "model", OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Modes:      []string{"chat"},
						Categories: []core.ModelCategory{core.CategoryTextGeneration},
					},
				},
				{
					ID: "text-embedding-3-small", Object: "model", OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Modes:      []string{"embedding"},
						Categories: []core.ModelCategory{core.CategoryEmbedding},
					},
				},
				{
					ID: "dall-e-3", Object: "model", OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Modes:      []string{"image_generation"},
						Categories: []core.ModelCategory{core.CategoryImage},
					},
				},
				{
					ID: "no-metadata", Object: "model", OwnedBy: "openai",
				},
			},
		},
	}
	registry.RegisterProviderWithType(mock, "openai")
	_ = registry.Initialize(context.Background())

	t.Run("FilterTextGeneration", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryTextGeneration)
		require.Len(t, models, 1)
		assert.Equal(t, "gpt-4o", models[0].Model.ID)
	})

	t.Run("FilterEmbedding", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryEmbedding)
		require.Len(t, models, 1)
		assert.Equal(t, "text-embedding-3-small", models[0].Model.ID)
	})

	t.Run("FilterImage", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryImage)
		require.Len(t, models, 1)
	})

	t.Run("FilterAll", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryAll)
		require.Len(t, models, 4)
	})

	t.Run("FilterEmpty", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryVideo)
		require.Empty(t, models)
	})
}

func TestListModelsWithProviderByCategory_UsesStoredProviderMetadata(t *testing.T) {
	registry := NewModelRegistry()
	registry.modelsByProvider = map[string]map[string]*ModelInfo{
		"internal-provider-key": {
			"gpt-4o": {
				Model: core.Model{
					ID: "gpt-4o",
					Metadata: &core.ModelMetadata{
						Categories: []core.ModelCategory{core.CategoryTextGeneration},
					},
				},
				ProviderName: "public-openai",
				ProviderType: "openai",
			},
		},
	}

	allModels := registry.ListModelsWithProvider()
	require.Len(t, allModels, 1)

	filtered := registry.ListModelsWithProviderByCategory(core.CategoryTextGeneration)
	require.Len(t, filtered, 1)
	require.Equal(t, allModels[0].ProviderName, filtered[0].ProviderName)
	require.Equal(t, allModels[0].ProviderType, filtered[0].ProviderType)
	require.Equal(t, "public-openai/gpt-4o", filtered[0].Selector)
}

func TestGetCategoryCounts_CountsProviderBackedModels(t *testing.T) {
	registry := NewModelRegistry()

	openAI := &registryMockProvider{
		name: "provider-openai",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:      "gpt-4o",
					Object:  "model",
					OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Categories: []core.ModelCategory{core.CategoryTextGeneration},
					},
				},
			},
		},
	}
	openRouter := &registryMockProvider{
		name: "provider-openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:      "gpt-4o",
					Object:  "model",
					OwnedBy: "openai",
					Metadata: &core.ModelMetadata{
						Categories: []core.ModelCategory{core.CategoryTextGeneration},
					},
				},
			},
		},
	}

	registry.RegisterProviderWithNameAndType(openAI, "openai", "openai")
	registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	counts := registry.GetCategoryCounts()
	var gotAll, gotTextGeneration int
	for _, count := range counts {
		switch count.Category {
		case core.CategoryAll:
			gotAll = count.Count
		case core.CategoryTextGeneration:
			gotTextGeneration = count.Count
		}
	}
	require.Equal(t, 2, gotAll)
	require.Equal(t, 2, gotTextGeneration)
}

func TestGetCategoryCounts(t *testing.T) {
	registry := NewModelRegistry()
	mock := &registryMockProvider{
		name: "test",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID: "gpt-4o", Object: "model",
					Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryTextGeneration}},
				},
				{
					ID: "gpt-4o-mini", Object: "model",
					Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryTextGeneration}},
				},
				{
					ID: "text-embedding-3-small", Object: "model",
					Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryEmbedding}},
				},
				{
					ID: "dall-e-3", Object: "model",
					Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryImage}},
				},
				{
					ID: "no-metadata", Object: "model",
				},
			},
		},
	}
	registry.RegisterProviderWithType(mock, "openai")
	_ = registry.Initialize(context.Background())

	counts := registry.GetCategoryCounts()

	// Should have entries for all categories
	require.Equal(t, len(core.AllCategories()), len(counts))

	// Verify specific counts
	countMap := make(map[core.ModelCategory]int)
	for _, c := range counts {
		countMap[c.Category] = c.Count
	}

	assert.Equal(t, 5, countMap[core.CategoryAll])
	assert.Equal(t, 2, countMap[core.CategoryTextGeneration])
	assert.Equal(t, 1, countMap[core.CategoryEmbedding])
	assert.Equal(t, 1, countMap[core.CategoryImage])
	assert.Equal(t, 0, countMap[core.CategoryAudio])

	// Verify ordering matches AllCategories()
	assert.Equal(t, core.CategoryAll, counts[0].Category)
	assert.Equal(t, core.CategoryTextGeneration, counts[1].Category)

	// Verify display names
	assert.Equal(t, "All", counts[0].DisplayName)
	assert.Equal(t, "Text Generation", counts[1].DisplayName)
}

// Verify ModelRegistry implements core.ModelLookup interface
var _ core.ModelLookup = (*ModelRegistry)(nil)

// audioRegistryMockProvider extends the mock with audio support so capability
// filtering can distinguish it from audio-less providers.
type audioRegistryMockProvider struct {
	registryMockProvider
}

func (m *audioRegistryMockProvider) CreateSpeech(_ context.Context, _ *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return &core.AudioResponse{}, nil
}

func (m *audioRegistryMockProvider) CreateTranscription(_ context.Context, _ *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return &core.AudioResponse{}, nil
}

func TestListPublicModels_HidesAudioOnlyModelsFromProvidersWithoutAudioSupport(t *testing.T) {
	inventory := func() *core.ModelsResponse {
		return &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "tts-model", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"audio_speech"}}},
				{ID: "stt-model", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"audio_transcription"}}},
				{ID: "chat-model", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"chat"}}},
				{ID: "bare-model", Object: "model"},
			},
		}
	}

	registry := NewModelRegistry()
	noAudio := &registryMockProvider{modelsResponse: inventory()}
	withAudio := &audioRegistryMockProvider{registryMockProvider{modelsResponse: inventory()}}
	registry.RegisterProviderWithNameAndType(noAudio, "gemini", "gemini")
	registry.RegisterProviderWithNameAndType(withAudio, "openai", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	got := make(map[string]bool)
	for _, model := range registry.ListPublicModels() {
		got[model.ID] = true
	}

	wantListed := []string{
		"gemini/chat-model", "gemini/bare-model", // no mode data or non-audio: kept
		"openai/chat-model", "openai/bare-model",
		"openai/tts-model", "openai/stt-model", // provider supports audio: kept
	}
	for _, id := range wantListed {
		assert.True(t, got[id], "expected %q to be listed", id)
	}
	for _, id := range []string{"gemini/tts-model", "gemini/stt-model"} {
		assert.False(t, got[id], "audio-only %q should be hidden (provider has no audio support)", id)
	}
}

// imageRegistryMockProvider extends the mock with image generation support so
// capability-based visibility can be exercised.
type imageRegistryMockProvider struct {
	registryMockProvider
}

func (m *imageRegistryMockProvider) CreateImage(_ context.Context, _ *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	return nil, nil
}

func TestListPublicModels_HidesImageOnlyModelsFromProvidersWithoutImageSupport(t *testing.T) {
	inventory := func() *core.ModelsResponse {
		return &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "image-model", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"image_generation", "image_edit"}}},
				{ID: "multimodal-model", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"chat", "image_generation"}}},
				{ID: "bare-model", Object: "model"},
			},
		}
	}

	registry := NewModelRegistry()
	noImages := &registryMockProvider{modelsResponse: inventory()}
	withImages := &imageRegistryMockProvider{registryMockProvider{modelsResponse: inventory()}}
	registry.RegisterProviderWithNameAndType(noImages, "gemini", "gemini")
	registry.RegisterProviderWithNameAndType(withImages, "openai", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	got := make(map[string]bool)
	for _, model := range registry.ListPublicModels() {
		got[model.ID] = true
	}

	wantListed := []string{
		"gemini/multimodal-model", "gemini/bare-model", // chat-capable or no mode data: kept
		"openai/multimodal-model", "openai/bare-model",
		"openai/image-model", // provider supports images: kept
	}
	for _, id := range wantListed {
		assert.True(t, got[id], "expected %q to be listed", id)
	}
	assert.False(t, got["gemini/image-model"], "image-only model should be hidden (provider has no image support)")
}

// TestProviderByTypeAndNameTrimConfiguredValues verifies that configured provider
// names and types are normalized at registration so lookups succeed even when the
// configured value arrives padded with whitespace (e.g. from YAML or env vars).
func TestProviderByTypeAndNameTrimConfiguredValues(t *testing.T) {
	registry := NewModelRegistry()
	mock := &registryMockProvider{name: "padded"}
	registry.RegisterProviderWithNameAndType(mock, "  padded-name  ", "  openai  ")
	got := registry.ProviderByType("openai")
	require.Equal(t, mock, got)
	got = registry.ProviderByName("padded-name")
	require.Equal(t, mock, got)
	require.Equal(t, "openai", registry.GetProviderTypeForName("padded-name"))
	require.Equal(t, "padded-name", registry.GetProviderNameForType("openai"))
}

func TestRefreshModelList_ConditionalFetch(t *testing.T) {
	const etag = `"list-v1"`
	body := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {"openai": {"display_name": "OpenAI", "api_type": "openai"}},
		"models": {"test-model": {"display_name": "Test Model", "modes": ["chat"]}},
		"provider_models": {}
	}`)

	var fullFetches, notModified atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fullFetches.Add(1)
		w.Header().Set("ETag", etag)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	registry := NewModelRegistry()

	count, err := registry.RefreshModelList(context.Background(), server.URL)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, int64(1), fullFetches.Load())
	require.Equal(t, int64(0), notModified.Load())

	registry.mu.RLock()
	listBefore := registry.modelList
	registry.mu.RUnlock()

	count, err = registry.RefreshModelList(context.Background(), server.URL)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, int64(1), notModified.Load(), "expected second fetch to be answered 304, got full=%d notModified=%d", fullFetches.Load(), notModified.Load())

	registry.mu.RLock()
	listAfter := registry.modelList
	etagAfter := registry.modelListETag
	registry.mu.RUnlock()
	require.Equal(t, listBefore, listAfter)
	require.Equal(t, etag, etagAfter)
}

func TestRefreshModelList_ETagNotSentToDifferentURL(t *testing.T) {
	const etag = `"list-v1"`
	body := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {},
		"models": {"test-model": {"display_name": "Test Model", "modes": ["chat"]}},
		"provider_models": {}
	}`)

	newServer := func(counter *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("If-None-Match") != "" {
				counter.Add(1)
			}
			w.Header().Set("ETag", etag)
			_, _ = w.Write(body)
		}))
	}

	var firstConditional, secondConditional atomic.Int64
	first := newServer(&firstConditional)
	defer first.Close()
	second := newServer(&secondConditional)
	defer second.Close()

	registry := NewModelRegistry()
	_, err := registry.RefreshModelList(context.Background(), first.URL)
	require.NoError(t, err)
	_, err = registry.RefreshModelList(context.Background(), second.URL)
	require.NoError(t, err)
	require.Equal(t, int64(0), secondConditional.Load())
	got := registry.currentModelListETag(second.URL)
	require.Equal(t, etag, got)
	got = registry.currentModelListETag(first.URL)
	require.Empty(t, got)
}

func TestRefreshModelList_304AdoptsRefreshedETag(t *testing.T) {
	body := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {},
		"models": {"test-model": {"display_name": "Test Model", "modes": ["chat"]}},
		"provider_models": {}
	}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			// Same content, refreshed validator: RFC 9111 lets a 304 update
			// the stored ETag.
			w.Header().Set("ETag", `"list-v2"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"list-v1"`)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	registry := NewModelRegistry()
	_, err := registry.RefreshModelList(context.Background(), server.URL)
	require.NoError(t, err)
	_, err = registry.RefreshModelList(context.Background(), server.URL)
	require.NoError(t, err)
	got := registry.currentModelListETag(server.URL)
	require.Equal(t, `"list-v2"`, got)
}

func TestSetModelList_ClearsETag(t *testing.T) {
	registry := NewModelRegistry()
	raw := []byte(`{"version": 1, "providers": {}, "models": {}, "provider_models": {}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.setModelListAndEnrich(list, raw, `"old"`, "https://example.test/models.min.json")
	registry.SetModelList(list, raw)
	got := registry.currentModelListETag("https://example.test/models.min.json")
	require.Empty(t, got)
}
