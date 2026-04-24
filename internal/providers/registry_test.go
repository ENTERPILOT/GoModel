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

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/modeldata"
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

		if registry.ProviderCount() != 1 {
			t.Errorf("expected 1 provider, got %d", registry.ProviderCount())
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if registry.ModelCount() != 2 {
			t.Errorf("expected 2 models, got %d", registry.ModelCount())
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if registry.ModelCount() != 2 {
			t.Fatalf("ModelCount() = %d, want 2", registry.ModelCount())
		}
		if !registry.Supports("upstream-extra") {
			t.Fatal("expected fallback mode to keep upstream-extra when upstream models are available")
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if registry.ModelCount() != 2 {
			t.Fatalf("ModelCount() = %d, want 2", registry.ModelCount())
		}
		if !registry.Supports("configured-model") || !registry.Supports("fallback-only") {
			t.Fatalf("expected configured fallback models to be registered, got %+v", registry.ListModels())
		}
		model := registry.GetModel("configured-model")
		if model == nil {
			t.Fatal("expected configured-model to resolve")
		}
		if model.Model.Object != "model" {
			t.Fatalf("Object = %q, want model", model.Model.Object)
		}
		if model.Model.OwnedBy != "test" {
			t.Fatalf("OwnedBy = %q, want test", model.Model.OwnedBy)
		}
		if model.Model.Created <= 0 {
			t.Fatalf("Created = %d, want non-zero configured fallback timestamp", model.Model.Created)
		}
		snapshots := registry.ProviderRuntimeSnapshots()
		if len(snapshots) != 1 {
			t.Fatalf("expected 1 provider runtime snapshot, got %d", len(snapshots))
		}
		if !strings.Contains(snapshots[0].LastModelFetchError, "models unavailable") {
			t.Fatalf("LastModelFetchError = %q, want upstream error preserved", snapshots[0].LastModelFetchError)
		}
		if snapshots[0].LastModelFetchSuccessAt == nil {
			t.Fatal("expected LastModelFetchSuccessAt to be recorded for configured fallback inventory")
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if listCount.Load() != 0 {
			t.Fatalf("ListModels calls = %d, want 0", listCount.Load())
		}
		if registry.ModelCount() != 2 {
			t.Fatalf("ModelCount() = %d, want 2", registry.ModelCount())
		}
		if registry.Supports("upstream-extra") {
			t.Fatal("expected allowlist mode to hide upstream-extra")
		}
		configured := registry.GetModel("configured-model")
		if configured == nil {
			t.Fatal("expected configured-model to resolve")
		}
		if configured.Model.Created <= 0 || configured.Model.OwnedBy != "test-type" {
			t.Fatalf("configured metadata = %+v, want configured-only metadata", configured.Model)
		}
		missing := registry.GetModel("missing-configured")
		if missing == nil {
			t.Fatal("expected missing-configured to be added")
		}
		if missing.Model.OwnedBy != "test-type" {
			t.Fatalf("OwnedBy = %q, want test-type", missing.Model.OwnedBy)
		}
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if listCount.Load() != 1 {
			t.Fatalf("ListModels calls = %d, want 1", listCount.Load())
		}
		if !registry.Supports("upstream-model") {
			t.Fatal("expected upstream-model to resolve when provider has no configured models")
		}
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
		if provider != mock {
			t.Error("expected to get the registered provider")
		}

		provider = registry.GetProvider("unknown-model")
		if provider != nil {
			t.Error("expected nil for unknown model")
		}
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

		if !registry.Supports("test-model") {
			t.Error("expected Supports to return true for registered model")
		}

		if registry.Supports("unknown-model") {
			t.Error("expected Supports to return false for unknown model")
		}
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
		if modelInfo == nil {
			t.Fatal("expected ModelInfo for registered model, got nil")
		}
		if modelInfo.Model.ID != expectedModel.ID {
			t.Errorf("expected model ID %q, got %q", expectedModel.ID, modelInfo.Model.ID)
		}
		if modelInfo.Model.OwnedBy != expectedModel.OwnedBy {
			t.Errorf("expected model OwnedBy %q, got %q", expectedModel.OwnedBy, modelInfo.Model.OwnedBy)
		}
		if modelInfo.Model.Created != expectedModel.Created {
			t.Errorf("expected model Created %d, got %d", expectedModel.Created, modelInfo.Model.Created)
		}
		if modelInfo.Provider != mock {
			t.Error("expected Provider to be the registered mock provider")
		}

		unknownInfo := registry.GetModel("unknown-model")
		if unknownInfo != nil {
			t.Errorf("expected nil for unknown model, got %+v", unknownInfo)
		}
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
		if before == nil {
			t.Fatal("expected GetModel to return a published ModelInfo")
		}
		if before.Model.Metadata != nil {
			t.Fatalf("expected initial metadata to be nil, got %#v", before.Model.Metadata)
		}

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
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
		registry.SetModelList(list, raw)
		registry.EnrichModels()

		if before.Model.Metadata != nil {
			t.Fatalf("expected previously published ModelInfo to remain unchanged, got %#v", before.Model.Metadata)
		}

		after := registry.GetModel("test-model")
		if after == nil {
			t.Fatal("expected GetModel to return an enriched ModelInfo")
		}
		if after == before {
			t.Fatal("expected EnrichModels to replace the published ModelInfo pointer")
		}
		if after.Model.Metadata == nil {
			t.Fatal("expected enriched metadata to be present")
		}
		if after.Model.Metadata.DisplayName != "Test Model" {
			t.Fatalf("registry display name = %q, want Test Model", after.Model.Metadata.DisplayName)
		}

		lookup, ok := registry.LookupModel("test-model")
		if !ok || lookup == nil {
			t.Fatal("expected LookupModel to return the enriched model")
		}
		if lookup.Metadata == nil {
			t.Fatal("expected LookupModel metadata to be present")
		}
		if lookup.Metadata.DisplayName != "Test Model" {
			t.Fatalf("lookup display name = %q, want Test Model", lookup.Metadata.DisplayName)
		}
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
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}

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
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
		registry.SetModelList(list, raw)
		registry.EnrichModels()

		if registry.ModelCount() != 1 {
			t.Fatalf("ModelCount() = %d, want 1", registry.ModelCount())
		}
		if synthetic := registry.GetModel("claude-4-opus"); synthetic != nil {
			t.Fatalf("expected canonical alias target to NOT be materialized, got %+v", synthetic)
		}

		info := registry.GetModel("claude-opus-4")
		if info == nil {
			t.Fatal("expected upstream model ID to remain registered")
		}
		if info.Model.ID != "claude-opus-4" {
			t.Fatalf("Model.ID = %q, want claude-opus-4", info.Model.ID)
		}
		if info.Model.Metadata == nil {
			t.Fatal("expected metadata to be enriched via alias")
		}
		if info.Model.Metadata.DisplayName != "Claude 4 Opus" {
			t.Fatalf("DisplayName = %q, want Claude 4 Opus", info.Model.Metadata.DisplayName)
		}
		if info.Model.Metadata.ContextWindow == nil || *info.Model.Metadata.ContextWindow != 200000 {
			t.Fatalf("ContextWindow = %v, want 200000", info.Model.Metadata.ContextWindow)
		}
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
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}

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
		if err != nil {
			t.Fatalf("RefreshModelList() error = %v", err)
		}
		if count != 1 {
			t.Fatalf("RefreshModelList() count = %d, want 1", count)
		}

		info := registry.GetModel("gpt-test")
		if info == nil || info.Model.Metadata == nil {
			t.Fatal("expected refreshed model metadata")
		}
		if info.Model.Metadata.DisplayName != "GPT Test" {
			t.Fatalf("DisplayName = %q, want GPT Test", info.Model.Metadata.DisplayName)
		}
		if !info.Model.Metadata.Capabilities["tool_calling"] {
			t.Fatal("expected tool_calling capability from refreshed model list")
		}
	})

	t.Run("InitializeReturnsGatewayErrorWhenContextCanceledBeforeAcquire", func(t *testing.T) {
		registry := NewModelRegistry()
		ch := registry.refreshSemaphore()
		ch <- struct{}{}
		defer func() { <-ch }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := registry.Initialize(ctx)
		if err == nil {
			t.Fatal("Initialize() error = nil, want cancellation error")
		}

		var gatewayErr *core.GatewayError
		if !errors.As(err, &gatewayErr) {
			t.Fatalf("Initialize() error = %T, want *core.GatewayError", err)
		}
		if gatewayErr.HTTPStatusCode() != http.StatusRequestTimeout {
			t.Fatalf("status = %d, want 408", gatewayErr.HTTPStatusCode())
		}
		if gatewayErr.Provider != "model_registry" {
			t.Fatalf("provider = %q, want model_registry", gatewayErr.Provider)
		}
	})

	t.Run("RefreshModelListReturnsGatewayErrorWhenContextCanceledBeforeAcquire", func(t *testing.T) {
		registry := NewModelRegistry()
		ch := registry.refreshSemaphore()
		ch <- struct{}{}
		defer func() { <-ch }()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := registry.RefreshModelList(ctx, "https://example.test/models.min.json")
		if err == nil {
			t.Fatal("RefreshModelList() error = nil, want cancellation error")
		}

		var gatewayErr *core.GatewayError
		if !errors.As(err, &gatewayErr) {
			t.Fatalf("RefreshModelList() error = %T, want *core.GatewayError", err)
		}
		if gatewayErr.HTTPStatusCode() != http.StatusRequestTimeout {
			t.Fatalf("status = %d, want 408", gatewayErr.HTTPStatusCode())
		}
		if gatewayErr.Provider != "model_registry" {
			t.Fatalf("provider = %q, want model_registry", gatewayErr.Provider)
		}
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

		if registry.ModelCount() != 1 {
			t.Errorf("expected 1 model (deduplicated), got %d", registry.ModelCount())
		}

		provider := registry.GetProvider("shared-model")
		if provider != mock1 {
			t.Error("expected first provider to win for duplicate model")
		}

		if provider := registry.GetProvider("provider2/shared-model"); provider != mock2 {
			t.Error("expected qualified lookup to resolve second provider")
		}
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
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		router, err := NewRouter(registry)
		if err != nil {
			t.Fatalf("unexpected router error: %v", err)
		}

		resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "google/gemini-xyz"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.ID != "openrouter" {
			t.Fatalf("resp.ID = %q, want openrouter", resp.ID)
		}
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
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		router, err := NewRouter(registry)
		if err != nil {
			t.Fatalf("unexpected router error: %v", err)
		}

		_, err = router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "google/gemini-xyz"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusNotFound {
			t.Fatalf("expected 404 status, got %d", gwErr.HTTPStatusCode())
		}
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
		if err == nil {
			t.Error("expected error when all providers fail, got nil")
		}

		expectedMsg := "failed to fetch models from any provider"
		if err.Error() != expectedMsg {
			t.Errorf("expected error message '%s', got '%s'", expectedMsg, err.Error())
		}
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
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("initial Initialize() error = %v", err)
		}

		mock.err = errors.New("refresh error")
		err := registry.Initialize(context.Background())
		if err == nil {
			t.Fatal("expected failed refresh to return an error")
		}

		snapshots := registry.ProviderRuntimeSnapshots()
		if len(snapshots) != 1 {
			t.Fatalf("expected 1 provider runtime snapshot, got %d", len(snapshots))
		}
		snapshot := snapshots[0]
		if snapshot.DiscoveredModelCount != 1 {
			t.Fatalf("expected previous model inventory to remain available, got %d models", snapshot.DiscoveredModelCount)
		}
		if !strings.Contains(snapshot.LastModelFetchError, "refresh error") {
			t.Fatalf("LastModelFetchError = %q, want refresh error", snapshot.LastModelFetchError)
		}
		if snapshot.LastModelFetchAt == nil {
			t.Fatal("expected LastModelFetchAt to be recorded")
		}
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
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("initial Initialize() error = %v", err)
		}

		mock.modelsResponse = &core.ModelsResponse{Object: "list"}
		err := registry.Initialize(context.Background())
		if err == nil {
			t.Fatal("expected empty refresh to return an error")
		}

		snapshots := registry.ProviderRuntimeSnapshots()
		if len(snapshots) != 1 {
			t.Fatalf("expected 1 provider runtime snapshot, got %d", len(snapshots))
		}
		snapshot := snapshots[0]
		if snapshot.DiscoveredModelCount != 1 {
			t.Fatalf("expected previous model inventory to remain available, got %d models", snapshot.DiscoveredModelCount)
		}
		if !strings.Contains(snapshot.LastModelFetchError, "empty model list") {
			t.Fatalf("LastModelFetchError = %q, want empty model list error", snapshot.LastModelFetchError)
		}
		if snapshot.LastModelFetchAt == nil {
			t.Fatal("expected LastModelFetchAt to be recorded")
		}
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
			if len(models) != 3 {
				t.Fatalf("expected 3 models, got %d", len(models))
			}

			if models[0].ID != "alpha-model" {
				t.Errorf("expected first model to be 'alpha-model', got '%s'", models[0].ID)
			}
			if models[1].ID != "middle-model" {
				t.Errorf("expected second model to be 'middle-model', got '%s'", models[1].ID)
			}
			if models[2].ID != "zebra-model" {
				t.Errorf("expected third model to be 'zebra-model', got '%s'", models[2].ID)
			}
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

		if !registry.Supports("test-model") {
			t.Fatal("expected model to be available before refresh")
		}

		err := registry.Refresh(context.Background())
		if err != nil {
			t.Fatalf("unexpected refresh error: %v", err)
		}

		if !registry.Supports("test-model") {
			t.Error("expected model to be available after refresh")
		}
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
		if pType != "openai" {
			t.Errorf("expected provider type 'openai', got '%s'", pType)
		}

		pType = registry.GetProviderType("unknown-model")
		if pType != "" {
			t.Errorf("expected empty provider type for unknown model, got '%s'", pType)
		}
	})
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
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	registry.SetModelList(list, raw)

	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() {
		slog.SetDefault(original)
	})

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	logs := buf.String()
	if got := strings.Count(logs, `"msg":"enriched models with metadata"`); got != 0 {
		t.Fatalf("expected no standalone enrichment info logs, got %d:\n%s", got, logs)
	}
	if got := strings.Count(logs, `"msg":"model registry initialized"`); got != 1 {
		t.Fatalf("expected one initialization summary log, got %d:\n%s", got, logs)
	}
	if !strings.Contains(logs, `"metadata_enriched":2`) {
		t.Fatalf("expected initialization log to include metadata_enriched=2:\n%s", logs)
	}
	if !strings.Contains(logs, `"metadata_total":2`) {
		t.Fatalf("expected initialization log to include metadata_total=2:\n%s", logs)
	}
	if !strings.Contains(logs, `"metadata_providers":2`) {
		t.Fatalf("expected initialization log to include metadata_providers=2:\n%s", logs)
	}
}

func TestListModelsWithProvider_Empty(t *testing.T) {
	registry := NewModelRegistry()
	models := registry.ListModelsWithProvider()
	if len(models) != 0 {
		t.Errorf("expected empty slice, got %d models", len(models))
	}
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
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	if models[0].Model.ID != "middle-model" {
		t.Errorf("expected first model middle-model, got %s", models[0].Model.ID)
	}
	if models[1].Model.ID != "alpha-model" {
		t.Errorf("expected second model alpha-model, got %s", models[1].Model.ID)
	}
	if models[2].Model.ID != "zebra-model" {
		t.Errorf("expected third model zebra-model, got %s", models[2].Model.ID)
	}
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
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	// Models are sorted: claude-3 before gpt-4
	if models[0].ProviderType != "anthropic" {
		t.Errorf("expected claude-3 provider type 'anthropic', got %q", models[0].ProviderType)
	}
	if models[1].ProviderType != "openai" {
		t.Errorf("expected gpt-4 provider type 'openai', got %q", models[1].ProviderType)
	}
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
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	registry.SetModelList(list, raw)

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	openAIInfo := registry.GetModel("openai-main/shared-model")
	if openAIInfo == nil || openAIInfo.Model.Metadata == nil {
		t.Fatal("expected openai-main/shared-model metadata to be present")
	}
	if openAIInfo.Model.Metadata.ContextWindow == nil || *openAIInfo.Model.Metadata.ContextWindow != 111111 {
		t.Fatalf("openai context window = %v, want 111111", openAIInfo.Model.Metadata.ContextWindow)
	}

	openRouterInfo := registry.GetModel("openrouter-main/shared-model")
	if openRouterInfo == nil || openRouterInfo.Model.Metadata == nil {
		t.Fatal("expected openrouter-main/shared-model metadata to be present")
	}
	if openRouterInfo.Model.Metadata.ContextWindow == nil || *openRouterInfo.Model.Metadata.ContextWindow != 222222 {
		t.Fatalf("openrouter context window = %v, want 222222", openRouterInfo.Model.Metadata.ContextWindow)
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models := registry.ListPublicModels()
	if len(models) != 3 {
		t.Fatalf("expected 3 public models, got %d", len(models))
	}

	want := []core.Model{
		{ID: "azure-openai/gpt-4o", OwnedBy: "azure-openai"},
		{ID: "openai/gpt-4o", OwnedBy: "openai"},
		{ID: "openrouter/gpt-4o", OwnedBy: "openrouter"},
	}
	for i, model := range want {
		if models[i].ID != model.ID {
			t.Fatalf("models[%d].ID = %q, want %q", i, models[i].ID, model.ID)
		}
		if models[i].OwnedBy != model.OwnedBy {
			t.Fatalf("models[%d].OwnedBy = %q, want %q", i, models[i].OwnedBy, model.OwnedBy)
		}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models := registry.ListModelsWithProvider()
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

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
		if models[i].Model.ID != wantModel.id {
			t.Fatalf("models[%d].Model.ID = %q, want %q", i, models[i].Model.ID, wantModel.id)
		}
		if models[i].ProviderName != wantModel.providerName {
			t.Fatalf("models[%d].ProviderName = %q, want %q", i, models[i].ProviderName, wantModel.providerName)
		}
		if models[i].ProviderType != wantModel.providerType {
			t.Fatalf("models[%d].ProviderType = %q, want %q", i, models[i].ProviderType, wantModel.providerType)
		}
		if models[i].Selector != wantModel.selector {
			t.Fatalf("models[%d].Selector = %q, want %q", i, models[i].Selector, wantModel.selector)
		}
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
		cancel := registry.StartBackgroundRefresh(interval, "")
		defer cancel()

		time.Sleep(interval*3 + 25*time.Millisecond)

		count := refreshCount.Load()
		if count < 2 {
			t.Errorf("expected at least 2 refreshes, got %d", count)
		}
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
		cancel := registry.StartBackgroundRefresh(interval, "")
		cancel()

		time.Sleep(interval * 3)

		count := refreshCount.Load()
		if count > 1 {
			t.Errorf("expected at most 1 refresh after cancel, got %d", count)
		}
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

			cancel := registry.StartBackgroundRefresh(10*time.Millisecond, "")
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

			cancel := registry.StartBackgroundRefresh(10*time.Millisecond, server.URL)
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
		cancel := registry.StartBackgroundRefresh(interval, "")
		defer cancel()

		time.Sleep(interval*3 + 25*time.Millisecond)

		count := refreshCount.Load()
		if count < 2 {
			t.Errorf("expected at least 2 refresh attempts despite errors, got %d", count)
		}
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
		if len(models) != 1 {
			t.Fatalf("expected 1 text_generation model, got %d", len(models))
		}
		if models[0].Model.ID != "gpt-4o" {
			t.Errorf("expected gpt-4o, got %s", models[0].Model.ID)
		}
	})

	t.Run("FilterEmbedding", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryEmbedding)
		if len(models) != 1 {
			t.Fatalf("expected 1 embedding model, got %d", len(models))
		}
		if models[0].Model.ID != "text-embedding-3-small" {
			t.Errorf("expected text-embedding-3-small, got %s", models[0].Model.ID)
		}
	})

	t.Run("FilterImage", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryImage)
		if len(models) != 1 {
			t.Fatalf("expected 1 image model, got %d", len(models))
		}
	})

	t.Run("FilterAll", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryAll)
		if len(models) != 4 {
			t.Fatalf("expected 4 models for 'all', got %d", len(models))
		}
	})

	t.Run("FilterEmpty", func(t *testing.T) {
		models := registry.ListModelsWithProviderByCategory(core.CategoryVideo)
		if len(models) != 0 {
			t.Fatalf("expected 0 video models, got %d", len(models))
		}
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
	if len(allModels) != 1 {
		t.Fatalf("expected 1 model from full listing, got %d", len(allModels))
	}

	filtered := registry.ListModelsWithProviderByCategory(core.CategoryTextGeneration)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 model from category listing, got %d", len(filtered))
	}
	if filtered[0].ProviderName != allModels[0].ProviderName {
		t.Fatalf("ProviderName = %q, want %q", filtered[0].ProviderName, allModels[0].ProviderName)
	}
	if filtered[0].ProviderType != allModels[0].ProviderType {
		t.Fatalf("ProviderType = %q, want %q", filtered[0].ProviderType, allModels[0].ProviderType)
	}
	if filtered[0].Selector != "public-openai/gpt-4o" {
		t.Fatalf("Selector = %q, want %q", filtered[0].Selector, "public-openai/gpt-4o")
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	if gotAll != 2 {
		t.Fatalf("all count = %d, want 2", gotAll)
	}
	if gotTextGeneration != 2 {
		t.Fatalf("text generation count = %d, want 2", gotTextGeneration)
	}
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
	if len(counts) != len(core.AllCategories()) {
		t.Fatalf("expected %d category counts, got %d", len(core.AllCategories()), len(counts))
	}

	// Verify specific counts
	countMap := make(map[core.ModelCategory]int)
	for _, c := range counts {
		countMap[c.Category] = c.Count
	}

	if countMap[core.CategoryAll] != 5 {
		t.Errorf("All count = %d, want 5", countMap[core.CategoryAll])
	}
	if countMap[core.CategoryTextGeneration] != 2 {
		t.Errorf("TextGeneration count = %d, want 2", countMap[core.CategoryTextGeneration])
	}
	if countMap[core.CategoryEmbedding] != 1 {
		t.Errorf("Embedding count = %d, want 1", countMap[core.CategoryEmbedding])
	}
	if countMap[core.CategoryImage] != 1 {
		t.Errorf("Image count = %d, want 1", countMap[core.CategoryImage])
	}
	if countMap[core.CategoryAudio] != 0 {
		t.Errorf("Audio count = %d, want 0", countMap[core.CategoryAudio])
	}

	// Verify ordering matches AllCategories()
	if counts[0].Category != core.CategoryAll {
		t.Errorf("first category = %q, want %q", counts[0].Category, core.CategoryAll)
	}
	if counts[1].Category != core.CategoryTextGeneration {
		t.Errorf("second category = %q, want %q", counts[1].Category, core.CategoryTextGeneration)
	}

	// Verify display names
	if counts[0].DisplayName != "All" {
		t.Errorf("All display name = %q, want %q", counts[0].DisplayName, "All")
	}
	if counts[1].DisplayName != "Text Generation" {
		t.Errorf("TextGeneration display name = %q, want %q", counts[1].DisplayName, "Text Generation")
	}
}

// Verify ModelRegistry implements core.ModelLookup interface
var _ core.ModelLookup = (*ModelRegistry)(nil)
