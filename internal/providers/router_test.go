package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// mockModelLookup implements core.ModelLookup for fast, isolated Router testing.
// This is simpler and faster than using a full ModelRegistry with providers.
type mockModelLookup struct {
	models        map[string]core.Provider
	providerTypes map[string]string
	modelList     []core.Model
	publicModels  []core.Model
	listCalls     int
	publicCalls   int
}

func newMockLookup() *mockModelLookup {
	return &mockModelLookup{
		models:        make(map[string]core.Provider),
		providerTypes: make(map[string]string),
	}
}

type registryModelEntry struct {
	provider     core.Provider
	providerName string
	providerType string
	modelID      string
}

func newTestRegistryWithModels(entries ...registryModelEntry) *ModelRegistry {
	registry := NewModelRegistry()
	for _, entry := range entries {
		registry.RegisterProviderWithNameAndType(entry.provider, entry.providerName, entry.providerType)
	}

	// Seed the unfiltered inventory; the routable maps are derived from it the
	// same way the production paths publish them.
	registry.discoveredByProvider = make(map[string]map[string]*ModelInfo)
	for _, entry := range entries {
		info := &ModelInfo{
			Model: core.Model{
				ID:     entry.modelID,
				Object: "model",
			},
			Provider:     entry.provider,
			ProviderName: entry.providerName,
			ProviderType: entry.providerType,
		}
		if _, ok := registry.discoveredByProvider[entry.providerName]; !ok {
			registry.discoveredByProvider[entry.providerName] = make(map[string]*ModelInfo)
		}
		registry.discoveredByProvider[entry.providerName][entry.modelID] = info
	}
	registry.publishFilteredInventoryLocked()
	return registry
}

func (m *mockModelLookup) addModel(model string, provider core.Provider, providerType string) {
	m.models[model] = provider
	m.providerTypes[model] = providerType
	m.modelList = append(m.modelList, core.Model{ID: model, Object: "model"})
}

func (m *mockModelLookup) setPublicModels(models []core.Model) {
	m.publicModels = append([]core.Model(nil), models...)
}

func (m *mockModelLookup) Supports(model string) bool {
	_, ok := m.models[model]
	return ok
}

func (m *mockModelLookup) GetProvider(model string) core.Provider {
	return m.models[model]
}

func (m *mockModelLookup) GetProviderType(model string) string {
	return m.providerTypes[model]
}

func (m *mockModelLookup) ListModels() []core.Model {
	m.listCalls++
	return m.modelList
}

func (m *mockModelLookup) ListPublicModels() []core.Model {
	m.publicCalls++
	return append([]core.Model(nil), m.publicModels...)
}

func (m *mockModelLookup) ModelCount() int {
	return len(m.models)
}

// The mock keeps no provider-name <-> type mapping, so the three resolver
// methods always return empty. Tests that need provider-name routing use the
// real ModelRegistry via newTestRegistryWithModels instead of this mock.
func (m *mockModelLookup) GetProviderName(_ string) string        { return "" }
func (m *mockModelLookup) GetProviderNameForType(_ string) string { return "" }
func (m *mockModelLookup) GetProviderTypeForName(_ string) string { return "" }

// mockProvider is a simple mock implementation of core.Provider for testing
type mockProvider struct {
	name              string
	chatResponse      *core.ChatResponse
	responsesResponse *core.ResponsesResponse
	embeddingResponse *core.EmbeddingResponse
	err               error
	lastChatReq       *core.ChatRequest
	lastResponsesReq  *core.ResponsesRequest
	lastEmbeddingReq  *core.EmbeddingRequest
	lastPassthrough   *core.PassthroughRequest
	passthroughResp   *core.PassthroughResponse
}

type mockAudioTranslationProvider struct {
	*mockProvider
	translationResponse *core.AudioResponse
	lastTranslationReq  *core.AudioTranscriptionRequest
}

func (m *mockAudioTranslationProvider) CreateTranslation(_ context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	m.lastTranslationReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.translationResponse, nil
}

func readAndCloseBody(t *testing.T, body io.ReadCloser) string {
	t.Helper()
	if body == nil {
		return ""
	}
	defer func() {
		_ = body.Close()
	}()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	return string(data)
}

func (m *mockProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	m.lastChatReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.chatResponse, nil
}

func (m *mockProvider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(nil), nil
}

func (m *mockProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return nil, nil
}

func (m *mockProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	m.lastResponsesReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.responsesResponse, nil
}

func (m *mockProvider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	if m.err != nil {
		return nil, m.err
	}
	return io.NopCloser(nil), nil
}

func (m *mockProvider) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	m.lastEmbeddingReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.embeddingResponse, nil
}

func (m *mockProvider) Passthrough(_ context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	m.lastPassthrough = req
	if m.err != nil {
		return nil, m.err
	}
	if m.passthroughResp != nil {
		return m.passthroughResp, nil
	}
	return &core.PassthroughResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

type lazyRefreshProvider struct {
	mockProvider
	modelsResponse    *core.ModelsResponse
	listModelsErr     error
	availabilityErr   error
	listModelsCalls   int
	availabilityCalls int
}

func (p *lazyRefreshProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	p.listModelsCalls++
	if p.listModelsErr != nil {
		return nil, p.listModelsErr
	}
	return p.modelsResponse, nil
}

func (p *lazyRefreshProvider) CheckAvailability(context.Context) error {
	p.availabilityCalls++
	return p.availabilityErr
}

type mockBatchProvider struct {
	mockProvider
	listBatchesResp    *core.BatchListResponse
	hintedBatchResults *core.BatchResultsResponse
	capturedBatchHints map[string]string
	capturedBatchID    string
	clearedBatchHintID string
	lastBatchReq       *core.BatchRequest
}

type mockResponseProvider struct {
	mockProvider
	lastInputTokensReq *core.ResponsesRequest
	lastCompactReq     *core.ResponsesRequest
	cancelledResponse  string
}

func (m *mockResponseProvider) GetResponse(_ context.Context, id string, _ core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	return &core.ResponsesResponse{ID: id, Object: "response", Status: "completed"}, nil
}

func (m *mockResponseProvider) ListResponseInputItems(_ context.Context, _ string, _ core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	return &core.ResponseInputItemListResponse{Object: "list"}, nil
}

func (m *mockResponseProvider) CancelResponse(_ context.Context, id string) (*core.ResponsesResponse, error) {
	m.cancelledResponse = id
	return &core.ResponsesResponse{ID: id, Object: "response", Status: "cancelled"}, nil
}

func (m *mockResponseProvider) DeleteResponse(_ context.Context, id string) (*core.ResponseDeleteResponse, error) {
	return &core.ResponseDeleteResponse{ID: id, Object: "response", Deleted: true}, nil
}

func (m *mockResponseProvider) CountResponseInputTokens(_ context.Context, req *core.ResponsesRequest) (*core.ResponseInputTokensResponse, error) {
	m.lastInputTokensReq = req
	return &core.ResponseInputTokensResponse{Object: "response.input_tokens", InputTokens: 42}, nil
}

func (m *mockResponseProvider) CompactResponse(_ context.Context, req *core.ResponsesRequest) (*core.ResponseCompactResponse, error) {
	m.lastCompactReq = req
	return &core.ResponseCompactResponse{ID: "cmp_1", Object: "response.compaction"}, nil
}

func (m *mockBatchProvider) CreateBatch(_ context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	m.lastBatchReq = req
	return &core.BatchResponse{ID: "provider-batch-1", Object: "batch"}, nil
}

func (m *mockBatchProvider) GetBatch(_ context.Context, _ string) (*core.BatchResponse, error) {
	return &core.BatchResponse{ID: "provider-batch-1", Object: "batch"}, nil
}

func (m *mockBatchProvider) ListBatches(_ context.Context, _ int, _ string) (*core.BatchListResponse, error) {
	if m.listBatchesResp != nil {
		return m.listBatchesResp, nil
	}
	return &core.BatchListResponse{Object: "list"}, nil
}

func (m *mockBatchProvider) CancelBatch(_ context.Context, _ string) (*core.BatchResponse, error) {
	return &core.BatchResponse{ID: "provider-batch-1", Object: "batch", Status: "cancelled"}, nil
}

func (m *mockBatchProvider) GetBatchResults(_ context.Context, _ string) (*core.BatchResultsResponse, error) {
	return &core.BatchResultsResponse{Object: "list", BatchID: "provider-batch-1"}, nil
}

func (m *mockBatchProvider) GetBatchResultsWithHints(_ context.Context, batchID string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	m.capturedBatchID = batchID
	if len(endpointByCustomID) > 0 {
		m.capturedBatchHints = make(map[string]string, len(endpointByCustomID))
		maps.Copy(m.capturedBatchHints, endpointByCustomID)
	}
	if m.hintedBatchResults != nil {
		return m.hintedBatchResults, nil
	}
	return m.GetBatchResults(context.Background(), "")
}

func (m *mockBatchProvider) ClearBatchResultHints(batchID string) {
	m.clearedBatchHintID = batchID
}

func (m *mockBatchProvider) CreateFile(_ context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	content := req.Content
	if req.ContentReader != nil {
		read, err := io.ReadAll(req.ContentReader)
		if err != nil {
			return nil, err
		}
		content = read
	}
	return &core.FileObject{
		ID:        "file_1",
		Object:    "file",
		Bytes:     int64(len(content)),
		CreatedAt: 1,
		Filename:  req.Filename,
		Purpose:   req.Purpose,
	}, nil
}

func (m *mockBatchProvider) ListFiles(_ context.Context, purpose string, _ int, _ string) (*core.FileListResponse, error) {
	return &core.FileListResponse{
		Object: "list",
		Data: []core.FileObject{
			{ID: "file_1", Object: "file", CreatedAt: 1, Filename: "a.jsonl", Purpose: purpose},
		},
	}, nil
}

func (m *mockBatchProvider) GetFile(_ context.Context, id string) (*core.FileObject, error) {
	return &core.FileObject{ID: id, Object: "file", CreatedAt: 1, Filename: "a.jsonl", Purpose: "batch"}, nil
}

func (m *mockBatchProvider) DeleteFile(_ context.Context, id string) (*core.FileDeleteResponse, error) {
	return &core.FileDeleteResponse{ID: id, Object: "file", Deleted: true}, nil
}

func (m *mockBatchProvider) GetFileContent(_ context.Context, id string) (*core.FileContentResponse, error) {
	return &core.FileContentResponse{ID: id, ContentType: "application/jsonl", Data: []byte("{}\n")}, nil
}

func TestNewRouter(t *testing.T) {
	t.Run("nil lookup returns error", func(t *testing.T) {
		router, err := NewRouter(nil)
		if err == nil {
			t.Error("expected error for nil lookup")
		}
		if router != nil {
			t.Error("expected nil router")
		}
	})

	t.Run("valid lookup succeeds", func(t *testing.T) {
		lookup := newMockLookup()
		router, err := NewRouter(lookup)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if router == nil {
			t.Error("expected non-nil router")
		}
	})
}

func TestRouterCreateTranslation(t *testing.T) {
	translator := &mockAudioTranslationProvider{
		mockProvider:        &mockProvider{name: "openai"},
		translationResponse: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hello"}`)},
	}
	lookup := newMockLookup()
	lookup.addModel("openai/whisper-1", translator, "openai")
	router, _ := NewRouter(lookup)

	resp, err := router.CreateTranslation(context.Background(), &core.AudioTranscriptionRequest{
		Model: "whisper-1", Provider: "openai", File: []byte("audio"), Prompt: "names",
	})
	if err != nil {
		t.Fatalf("CreateTranslation() error = %v", err)
	}
	if string(resp.Data) != `{"text":"hello"}` {
		t.Errorf("response = %s", resp.Data)
	}
	if translator.lastTranslationReq == nil {
		t.Fatal("translation provider was not called")
	}
	if translator.lastTranslationReq.Model != "whisper-1" || translator.lastTranslationReq.Provider != "" {
		t.Errorf("forwarded selector = %q/%q, want provider metadata stripped", translator.lastTranslationReq.Provider, translator.lastTranslationReq.Model)
	}
}

func TestRouterCreateTranslation_Errors(t *testing.T) {
	providerErr := errors.New("translation provider failed")
	tests := []struct {
		name         string
		model        string
		provider     core.Provider
		providerType string
		req          *core.AudioTranscriptionRequest
		wantError    string
		wantIs       error
	}{
		{
			name:         "unsupported provider",
			model:        "transcribe-only",
			provider:     &mockProvider{name: "cohere"},
			providerType: "cohere",
			req:          &core.AudioTranscriptionRequest{Model: "transcribe-only", File: []byte("audio")},
			wantError:    "does not support audio translations",
		},
		{
			name:         "provider failure",
			model:        "openai/whisper-1",
			provider:     &mockAudioTranslationProvider{mockProvider: &mockProvider{name: "openai", err: providerErr}},
			providerType: "openai",
			req:          &core.AudioTranscriptionRequest{Model: "whisper-1", Provider: "openai", File: []byte("audio")},
			wantIs:       providerErr,
		},
		{
			name:      "nil request",
			wantError: "audio translation request is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := newMockLookup()
			if tt.provider != nil {
				lookup.addModel(tt.model, tt.provider, tt.providerType)
			}
			router, err := NewRouter(lookup)
			if err != nil {
				t.Fatalf("NewRouter() error = %v", err)
			}

			_, err = router.CreateTranslation(context.Background(), tt.req)
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("CreateTranslation() error = %v, want %v", err, tt.wantIs)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("CreateTranslation() error = %v, want message containing %q", err, tt.wantError)
			}
		})
	}
}

func TestRouterEmptyLookup(t *testing.T) {
	lookup := newMockLookup() // Empty - no models
	router, _ := NewRouter(lookup)

	t.Run("Supports returns false", func(t *testing.T) {
		if router.Supports("any-model") {
			t.Error("expected false for empty lookup")
		}
	})

	t.Run("ChatCompletion returns error", func(t *testing.T) {
		_, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "any"})
		if !errors.Is(err, ErrRegistryNotInitialized) {
			t.Errorf("expected ErrRegistryNotInitialized, got: %v", err)
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
		}
	})

	t.Run("StreamChatCompletion returns error", func(t *testing.T) {
		_, err := router.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "any"})
		if !errors.Is(err, ErrRegistryNotInitialized) {
			t.Errorf("expected ErrRegistryNotInitialized, got: %v", err)
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
		}
	})

	t.Run("ListModels returns error", func(t *testing.T) {
		_, err := router.ListModels(context.Background())
		if !errors.Is(err, ErrRegistryNotInitialized) {
			t.Errorf("expected ErrRegistryNotInitialized, got: %v", err)
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
		}
	})

	t.Run("Responses returns error", func(t *testing.T) {
		_, err := router.Responses(context.Background(), &core.ResponsesRequest{Model: "any"})
		if !errors.Is(err, ErrRegistryNotInitialized) {
			t.Errorf("expected ErrRegistryNotInitialized, got: %v", err)
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
		}
	})

	t.Run("StreamResponses returns error", func(t *testing.T) {
		_, err := router.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "any"})
		if !errors.Is(err, ErrRegistryNotInitialized) {
			t.Errorf("expected ErrRegistryNotInitialized, got: %v", err)
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
		}
	})
}

func TestRouterSupports(t *testing.T) {
	openai := &mockProvider{name: "openai"}
	anthropic := &mockProvider{name: "anthropic"}

	lookup := newMockLookup()
	lookup.addModel("gpt-4o", openai, "openai")
	lookup.addModel("claude-3-5-sonnet", anthropic, "anthropic")

	router, _ := NewRouter(lookup)

	tests := []struct {
		model    string
		expected bool
	}{
		{"gpt-4o", true},
		{"claude-3-5-sonnet", true},
		{"unsupported", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := router.Supports(tt.model); got != tt.expected {
				t.Errorf("Supports(%q) = %v, want %v", tt.model, got, tt.expected)
			}
		})
	}
}

func TestRouterChatCompletion(t *testing.T) {
	openaiResp := &core.ChatResponse{ID: "openai-resp", Model: "gpt-4o"}
	anthropicResp := &core.ChatResponse{ID: "anthropic-resp", Model: "claude-3-5-sonnet"}

	openai := &mockProvider{name: "openai", chatResponse: openaiResp}
	anthropic := &mockProvider{name: "anthropic", chatResponse: anthropicResp}

	lookup := newMockLookup()
	lookup.addModel("gpt-4o", openai, "openai")
	lookup.addModel("claude-3-5-sonnet", anthropic, "anthropic")

	router, _ := NewRouter(lookup)

	tests := []struct {
		name         string
		model        string
		wantResp     *core.ChatResponse
		wantProvider string
		wantError    bool
	}{
		{"routes to openai", "gpt-4o", openaiResp, "openai", false},
		{"routes to anthropic", "claude-3-5-sonnet", anthropicResp, "anthropic", false},
		{"unsupported model", "unknown", nil, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{Model: tt.model}
			resp, err := router.ChatCompletion(context.Background(), req)

			if tt.wantError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				var gwErr *core.GatewayError
				if !errors.As(err, &gwErr) {
					t.Fatalf("expected GatewayError, got %T: %v", err, err)
				}
				if gwErr.HTTPStatusCode() != http.StatusNotFound {
					t.Fatalf("expected 404 status, got %d", gwErr.HTTPStatusCode())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if resp.ID != tt.wantResp.ID {
				t.Errorf("got response ID %q, want %q", resp.ID, tt.wantResp.ID)
			}
			if resp.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", resp.Provider, tt.wantProvider)
			}
		})
	}
}

// TestRouterChatCompletion_ResolvedRouteDispatch pins the resolvedRoute
// contract for both lookup shapes: a registry that serves provider, type, and
// name under one lock (modelInfoLookup) and a plain core.ModelLookup without
// that capability. Either way the upstream sees the concrete model with the
// gateway-only Provider hint stripped, and the response is stamped with the
// resolved provider type.
func TestRouterChatCompletion_ResolvedRouteDispatch(t *testing.T) {
	tests := []struct {
		name         string
		newLookup    func(provider core.Provider) core.ModelLookup
		wantInfoPath bool
		model        string
		providerHint string
	}{
		{
			name: "registry with modelInfoLookup",
			newLookup: func(provider core.Provider) core.ModelLookup {
				return newTestRegistryWithModels(registryModelEntry{
					provider:     provider,
					providerName: "openai-primary",
					providerType: "openai",
					modelID:      "gpt-4o",
				})
			},
			wantInfoPath: true,
			model:        "gpt-4o",
			providerHint: "openai-primary",
		},
		{
			name: "lookup without modelInfoLookup",
			newLookup: func(provider core.Provider) core.ModelLookup {
				lookup := newMockLookup()
				lookup.addModel("gpt-4o", provider, "openai")
				return lookup
			},
			wantInfoPath: false,
			model:        "gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockProvider{name: "upstream", chatResponse: &core.ChatResponse{ID: "resp", Model: "gpt-4o"}}
			lookup := tt.newLookup(provider)
			if _, ok := lookup.(modelInfoLookup); ok != tt.wantInfoPath {
				t.Fatalf("lookup implements modelInfoLookup = %v, want %v", ok, tt.wantInfoPath)
			}
			router, err := NewRouter(lookup)
			if err != nil {
				t.Fatalf("NewRouter: %v", err)
			}

			req := &core.ChatRequest{Model: tt.model, Provider: tt.providerHint}
			resp, err := router.ChatCompletion(context.Background(), req)
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}

			if provider.lastChatReq == nil {
				t.Fatal("provider was not called")
			}
			if got := provider.lastChatReq.Model; got != "gpt-4o" {
				t.Fatalf("forwarded model = %q, want concrete gpt-4o", got)
			}
			if got := provider.lastChatReq.Provider; got != "" {
				t.Fatalf("forwarded provider hint = %q, want empty", got)
			}
			if resp.Provider != "openai" {
				t.Fatalf("response provider = %q, want resolved provider type openai", resp.Provider)
			}
			if req.Model != tt.model || req.Provider != tt.providerHint {
				t.Fatalf("caller request mutated: %+v", req)
			}
		})
	}
}

func TestRouterChatCompletion_ProviderSelector(t *testing.T) {
	eastResp := &core.ChatResponse{ID: "east", Model: "gpt-4o"}
	westResp := &core.ChatResponse{ID: "west", Model: "gpt-4o"}
	east := &mockProvider{name: "openai-east", chatResponse: eastResp}
	west := &mockProvider{name: "openai-west", chatResponse: westResp}

	lookup := newMockLookup()
	lookup.addModel("gpt-4o", east, "openai")
	lookup.addModel("openai-west/gpt-4o", west, "openai")

	router, _ := NewRouter(lookup)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gpt-4o",
		Provider: "openai-west",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "west" {
		t.Fatalf("expected west provider response, got %q", resp.ID)
	}
	if west.lastChatReq == nil || west.lastChatReq.Model != "gpt-4o" {
		t.Fatalf("expected upstream model to be unqualified gpt-4o, got %#v", west.lastChatReq)
	}
	if west.lastChatReq.Provider != "" {
		t.Fatalf("expected provider field to be stripped upstream, got %q", west.lastChatReq.Provider)
	}
}

func TestRouterChatCompletion_AdaptsAnthropicCacheControlAfterRouting(t *testing.T) {
	tests := []struct {
		name            string
		providerType    string
		messagesIngress bool
		wantCache       bool
	}{
		{name: "messages ingress strips for openai", providerType: "openai", messagesIngress: true},
		{name: "chat ingress preserves for openai", providerType: "openai", wantCache: true},
		{name: "messages ingress preserves for anthropic", providerType: "anthropic", messagesIngress: true, wantCache: true},
		{name: "messages ingress preserves for openrouter", providerType: "openrouter", messagesIngress: true, wantCache: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheExtra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
				"x_keep":        json.RawMessage(`true`),
			})
			request := &core.ChatRequest{
				Model:       "gpt-4o",
				ExtraFields: cacheExtra,
				Tools: []map[string]any{{
					"type":          "function",
					"function":      map[string]any{"name": "lookup"},
					"cache_control": map[string]any{"type": "ephemeral"},
				}},
				Messages: []core.Message{
					{
						Role:        "assistant",
						ExtraFields: cacheExtra,
						Content: []core.ContentPart{{
							Type:        "text",
							Text:        "stable prefix",
							ExtraFields: cacheExtra,
						}},
						ToolCalls: []core.ToolCall{{
							ID:          "tool-1",
							Type:        "function",
							ExtraFields: cacheExtra,
							Function: core.FunctionCall{
								Name:        "lookup",
								Arguments:   `{}`,
								ExtraFields: cacheExtra,
							},
						}},
					},
					{
						Role:        "tool",
						ToolCallID:  "tool-1",
						ExtraFields: cacheExtra,
						Content: []core.ContentPart{{
							Type:        "text",
							Text:        "tool result",
							ExtraFields: cacheExtra,
						}},
					},
				},
			}
			before, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}

			provider := &mockProvider{name: tt.providerType, chatResponse: &core.ChatResponse{ID: "ok"}}
			lookup := newMockLookup()
			lookup.addModel("gpt-4o", provider, tt.providerType)
			router, _ := NewRouter(lookup)
			ctx := context.Background()
			if tt.messagesIngress {
				ctx = core.WithRequestDialect(ctx, core.RequestDialectAnthropicMessages)
			}
			if _, err := router.ChatCompletion(ctx, request); err != nil {
				t.Fatal(err)
			}

			forwarded := provider.lastChatReq
			if forwarded == nil {
				t.Fatal("provider did not receive a request")
			}
			expectedCache := json.RawMessage(`{"type":"ephemeral"}`)
			assertFields := func(label string, fields core.UnknownJSONFields, wantCache bool) {
				t.Helper()
				gotCache := fields.Lookup("cache_control")
				if wantCache && !bytes.Equal(gotCache, expectedCache) {
					t.Errorf("%s cache_control = %s, want %s", label, gotCache, expectedCache)
				}
				if !wantCache && len(gotCache) != 0 {
					t.Errorf("%s cache_control = %s, want absent", label, gotCache)
				}
				if got := string(fields.Lookup("x_keep")); got != "true" {
					t.Errorf("%s x_keep = %s, want preserved", label, got)
				}
			}
			assertFields("request", forwarded.ExtraFields, tt.wantCache)
			toolCache, toolHasCache := forwarded.Tools[0]["cache_control"]
			if tt.wantCache {
				encoded, err := json.Marshal(toolCache)
				if err != nil {
					t.Fatal(err)
				}
				if !toolHasCache || !bytes.Equal(encoded, expectedCache) {
					t.Errorf("tool cache_control = %s, want %s", encoded, expectedCache)
				}
			} else if toolHasCache {
				t.Errorf("tool cache_control = %v, want absent", toolCache)
			}
			assertFields("assistant message", forwarded.Messages[0].ExtraFields, tt.wantCache)
			assertFields("assistant content", forwarded.Messages[0].Content.([]core.ContentPart)[0].ExtraFields, tt.wantCache)
			call := forwarded.Messages[0].ToolCalls[0]
			assertFields("tool call", call.ExtraFields, tt.wantCache)
			assertFields("function call", call.Function.ExtraFields, tt.wantCache)
			assertFields("tool result", forwarded.Messages[1].ExtraFields, tt.wantCache)
			assertFields("tool result content", forwarded.Messages[1].Content.([]core.ContentPart)[0].ExtraFields, tt.wantCache)

			assertFields("caller request", request.ExtraFields, true)
			assertFields("caller assistant message", request.Messages[0].ExtraFields, true)
			assertFields("caller assistant content", request.Messages[0].Content.([]core.ContentPart)[0].ExtraFields, true)
			callerCall := request.Messages[0].ToolCalls[0]
			assertFields("caller tool call", callerCall.ExtraFields, true)
			assertFields("caller function call", callerCall.Function.ExtraFields, true)
			assertFields("caller tool result", request.Messages[1].ExtraFields, true)
			assertFields("caller tool result content", request.Messages[1].Content.([]core.ContentPart)[0].ExtraFields, true)

			after, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("routing mutated caller request\nbefore: %s\n after: %s", before, after)
			}
		})
	}
}

func TestRouterCreateBatch_AdaptsAnthropicCacheControlAfterRouting(t *testing.T) {
	tests := []struct {
		name            string
		providerType    string
		messagesIngress bool
		withHints       bool
		wantCache       bool
	}{
		{name: "messages ingress strips for openai", providerType: "openai", messagesIngress: true},
		{name: "messages ingress strips with hint-aware route", providerType: "openai", messagesIngress: true, withHints: true},
		{name: "generic batch preserves for openai", providerType: "openai", wantCache: true},
		{name: "messages ingress preserves for anthropic", providerType: "anthropic", messagesIngress: true, wantCache: true},
		{name: "messages ingress preserves for openrouter", providerType: "openrouter", messagesIngress: true, wantCache: true},
	}

	const body = `{
		"model":"gpt-4o",
		"cache_control":{"type":"ephemeral"},
		"x_keep":true,
		"messages":[{"role":"user","content":[{
			"type":"text","text":"stable prefix",
			"cache_control":{"type":"ephemeral"},"x_keep":true
		}]}],
		"tools":[{"type":"function","function":{"name":"lookup"},"cache_control":{"type":"ephemeral"}}]
	}`
	expectedCache := json.RawMessage(`{"type":"ephemeral"}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockBatchProvider{name: tt.providerType}
			lookup := newMockLookup()
			lookup.addModel("gpt-4o", provider, tt.providerType)
			router, _ := NewRouter(lookup)

			request := &core.BatchRequest{
				Endpoint: "/v1/chat/completions",
				Requests: []core.BatchRequestItem{{
					CustomID: "item-1",
					Method:   http.MethodPost,
					URL:      "/v1/chat/completions",
					Body:     json.RawMessage(body),
				}},
			}
			before := append(json.RawMessage(nil), request.Requests[0].Body...)
			ctx := context.Background()
			if tt.messagesIngress {
				ctx = core.WithRequestDialect(ctx, core.RequestDialectAnthropicMessages)
			}
			if tt.withHints {
				if _, _, err := router.CreateBatchWithHints(ctx, tt.providerType, request); err != nil {
					t.Fatal(err)
				}
			} else if _, err := router.CreateBatch(ctx, tt.providerType, request); err != nil {
				t.Fatal(err)
			}

			if provider.lastBatchReq == nil || len(provider.lastBatchReq.Requests) != 1 {
				t.Fatalf("provider batch request = %#v, want one item", provider.lastBatchReq)
			}
			decoded, err := core.DecodeKnownBatchItemRequest(provider.lastBatchReq.Endpoint, provider.lastBatchReq.Requests[0])
			if err != nil {
				t.Fatal(err)
			}
			chat := decoded.Request.(*core.ChatRequest)
			gotCache := chat.ExtraFields.Lookup("cache_control")
			if tt.wantCache && !bytes.Equal(gotCache, expectedCache) {
				t.Errorf("request cache_control = %s, want %s", gotCache, expectedCache)
			}
			if !tt.wantCache && len(gotCache) != 0 {
				t.Errorf("request cache_control = %s, want absent", gotCache)
			}
			if got := string(chat.ExtraFields.Lookup("x_keep")); got != "true" {
				t.Errorf("request x_keep = %s, want true", got)
			}
			part := chat.Messages[0].Content.([]core.ContentPart)[0]
			partCache := part.ExtraFields.Lookup("cache_control")
			if tt.wantCache && !bytes.Equal(partCache, expectedCache) {
				t.Errorf("content cache_control = %s, want %s", partCache, expectedCache)
			}
			if !tt.wantCache && len(partCache) != 0 {
				t.Errorf("content cache_control = %s, want absent", partCache)
			}
			if got := string(part.ExtraFields.Lookup("x_keep")); got != "true" {
				t.Errorf("content x_keep = %s, want true", got)
			}
			toolCache, toolHasCache := chat.Tools[0]["cache_control"]
			if tt.wantCache {
				encoded, err := json.Marshal(toolCache)
				if err != nil {
					t.Fatal(err)
				}
				if !toolHasCache || !bytes.Equal(encoded, expectedCache) {
					t.Errorf("tool cache_control = %s, want %s", encoded, expectedCache)
				}
			} else if toolHasCache {
				t.Errorf("tool cache_control = %v, want absent", toolCache)
			}

			if !bytes.Equal(request.Requests[0].Body, before) {
				t.Fatalf("routing mutated caller batch item\nbefore: %s\n after: %s", before, request.Requests[0].Body)
			}
		})
	}
}

func TestAdaptAnthropicCacheControl_PreservesSupportedProviders(t *testing.T) {
	req := &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
	})}
	for _, providerType := range []string{"anthropic", "openrouter"} {
		if got := adaptAnthropicCacheControl(req, providerType); got != req {
			t.Errorf("provider %q cloned or changed supported cache metadata", providerType)
		}
	}
}

func TestAdaptExtraContent_KeepsOwnVendorOnly(t *testing.T) {
	extra := func(members string) core.UnknownJSONFields {
		return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			core.ExtraContentField: json.RawMessage(`{"google":{"thought_signature":"sig"},"anthropic":{"is_error":true}}`),
			"x_keep":               json.RawMessage(members),
		})
	}
	req := &core.ChatRequest{Messages: []core.Message{
		{Role: "assistant", Content: "x", ExtraFields: extra("1"), ToolCalls: []core.ToolCall{{
			ID: "c1", Type: "function", ExtraFields: extra("2"),
			Function: core.FunctionCall{Name: "f", Arguments: "{}", ExtraFields: extra("3")},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: "boom", ExtraFields: extra("4")},
	}}
	tests := []struct {
		providerType string
		want         string
	}{
		{providerType: "gemini", want: `{"google":{"thought_signature":"sig"}}`},
		{providerType: "vertex", want: `{"google":{"thought_signature":"sig"}}`},
		{providerType: "Anthropic", want: `{"anthropic":{"is_error":true}}`},
		{providerType: "openai", want: ""},
		{providerType: "openrouter", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.providerType, func(t *testing.T) {
			got := adaptExtraContent(req, tt.providerType)
			if got == req {
				t.Fatal("request was not adapted")
			}
			call := got.Messages[0].ToolCalls[0]
			for name, fields := range map[string]core.UnknownJSONFields{
				"assistant": got.Messages[0].ExtraFields,
				"tool call": call.ExtraFields,
				"function":  call.Function.ExtraFields,
				"tool":      got.Messages[1].ExtraFields,
			} {
				if raw := string(fields.Lookup(core.ExtraContentField)); raw != tt.want {
					t.Errorf("%s extra_content = %s, want %s", name, raw, tt.want)
				}
				if raw := fields.Lookup("x_keep"); len(raw) == 0 {
					t.Errorf("%s dropped unrelated extra", name)
				}
			}
		})
	}
	if raw := req.Messages[0].ToolCalls[0].ExtraFields.ExtraContent(core.ExtraContentVendorAnthropic); len(raw) == 0 {
		t.Error("adaptation mutated the caller's request")
	}
}

func TestAdaptExtraContent_ReturnsRequestWithoutExtraContent(t *testing.T) {
	req := &core.ChatRequest{Messages: []core.Message{
		{Role: "assistant", Content: "x", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"x_keep": json.RawMessage("true"),
		}), ToolCalls: []core.ToolCall{{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"}}}},
	}}
	if got := adaptExtraContent(req, "openai"); got != req {
		t.Error("request without extra_content must be returned as-is")
	}
	own := &core.ChatRequest{Messages: []core.Message{
		{Role: "assistant", ContentNull: true, ToolCalls: []core.ToolCall{{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"google":{"thought_signature":"sig"}}`),
			})}}},
	}}
	if got := adaptExtraContent(own, "gemini"); got != own {
		t.Error("request carrying only the provider's own extra_content must be returned as-is")
	}
	if got := adaptExtraContent(nil, "openai"); got != nil {
		t.Error("nil request must stay nil")
	}
}

func TestForwardChatRequest_DropsForeignExtraContentForEveryDialect(t *testing.T) {
	req := &core.ChatRequest{Model: "m", Provider: "p", Messages: []core.Message{
		{Role: "assistant", ContentNull: true, ToolCalls: []core.ToolCall{{
			ID: "c1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"google":{"thought_signature":"sig"}}`),
			}),
		}}},
	}}
	route := resolvedRoute{selector: core.ModelSelector{Model: "m"}, providerType: "openai"}
	for _, ctx := range []context.Context{
		context.Background(),
		core.WithRequestDialect(context.Background(), core.RequestDialectAnthropicMessages),
	} {
		got := forwardChatRequest(ctx, req, route)
		if raw := got.Messages[0].ToolCalls[0].ExtraFields.Lookup(core.ExtraContentField); len(raw) != 0 {
			t.Errorf("openai received foreign extra_content: %s", raw)
		}
		if got.Provider != "" {
			t.Errorf("provider hint = %q, want cleared", got.Provider)
		}
	}
	route.providerType = "gemini"
	got := forwardChatRequest(context.Background(), req, route)
	if raw := got.Messages[0].ToolCalls[0].ExtraFields.ExtraContent(core.ExtraContentVendorGoogle); len(raw) == 0 {
		t.Error("gemini lost its own extra_content")
	}
}

func TestRouterChatCompletion_PrefixedModelSelector(t *testing.T) {
	westResp := &core.ChatResponse{ID: "west", Model: "gpt-4o"}
	west := &mockProvider{name: "openai-west", chatResponse: westResp}

	lookup := newMockLookup()
	lookup.addModel("openai-west/gpt-4o", west, "openai")

	router, _ := NewRouter(lookup)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "openai-west/gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "west" {
		t.Fatalf("expected west provider response, got %q", resp.ID)
	}
	if west.lastChatReq == nil || west.lastChatReq.Model != "gpt-4o" {
		t.Fatalf("expected upstream model to be unqualified gpt-4o, got %#v", west.lastChatReq)
	}
}

func TestRouterChatCompletion_RefreshesProviderModelsForQualifiedRequest(t *testing.T) {
	provider := &lazyRefreshProvider{
		name:         "ollama",
		chatResponse: &core.ChatResponse{ID: "chatcmpl-later", Model: "later-model"},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "later-model", Object: "model", OwnedBy: "ollama"},
			},
		},
	}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "ollama", "ollama")

	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/later-model"})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want nil", err)
	}
	if resp.ID != "chatcmpl-later" {
		t.Fatalf("response ID = %q, want chatcmpl-later", resp.ID)
	}
	if provider.availabilityCalls != 1 {
		t.Fatalf("availability calls = %d, want 1", provider.availabilityCalls)
	}
	if provider.listModelsCalls != 1 {
		t.Fatalf("ListModels calls = %d, want 1", provider.listModelsCalls)
	}
	if provider.lastChatReq == nil || provider.lastChatReq.Model != "later-model" {
		t.Fatalf("expected upstream model later-model, got %#v", provider.lastChatReq)
	}
	if !registry.Supports("ollama/later-model") {
		t.Fatal("expected request-time refresh to register ollama/later-model")
	}
}

func TestRouterChatCompletion_RefreshesMissingProviderWithoutDroppingExistingModels(t *testing.T) {
	openAI := &mockProvider{
		name:         "openai",
		chatResponse: &core.ChatResponse{ID: "openai", Model: "gpt-4o"},
	}
	ollama := &lazyRefreshProvider{
		name:         "ollama",
		chatResponse: &core.ChatResponse{ID: "ollama", Model: "local-model"},
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "local-model", Object: "model", OwnedBy: "ollama"},
			},
		},
	}
	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     openAI,
		providerName: "openai",
		providerType: "openai",
		modelID:      "gpt-4o",
	})
	registry.RegisterProviderWithNameAndType(ollama, "ollama", "ollama")

	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/local-model"})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want nil", err)
	}
	if resp.ID != "ollama" {
		t.Fatalf("response ID = %q, want ollama", resp.ID)
	}
	if !registry.Supports("openai/gpt-4o") {
		t.Fatal("expected existing openai model to remain after targeted refresh")
	}
	if !registry.Supports("ollama/local-model") {
		t.Fatal("expected targeted refresh to add ollama/local-model")
	}
}

func TestRouterChatCompletion_RequestTimeRefreshUnavailableProvider(t *testing.T) {
	provider := &lazyRefreshProvider{
		name:            "ollama",
		chatResponse:    &core.ChatResponse{ID: "should-not-run"},
		availabilityErr: errors.New("connection refused"),
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "later-model", Object: "model", OwnedBy: "ollama"},
			},
		},
	}
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "ollama", "ollama")

	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}

	_, err = router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/later-model"})
	if err == nil {
		t.Fatal("ChatCompletion() error = nil, want provider unavailable")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want GatewayError", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusServiceUnavailable)
	}
	if provider.availabilityCalls != 1 {
		t.Fatalf("availability calls = %d, want 1", provider.availabilityCalls)
	}
	if provider.listModelsCalls != 0 {
		t.Fatalf("ListModels calls = %d, want 0 when availability fails", provider.listModelsCalls)
	}
	if provider.lastChatReq != nil {
		t.Fatalf("provider call should not be attempted, got %#v", provider.lastChatReq)
	}
}

func TestRouterChatCompletion_PrefersProviderTypeSelectorOverRawSlashModel(t *testing.T) {
	openAIResp := &core.ChatResponse{ID: "openai-test", Model: "gpt-5-nano"}
	openRouterResp := &core.ChatResponse{ID: "openrouter", Model: "openai/gpt-5-nano"}
	openAI := &mockProvider{name: "openai_test", chatResponse: openAIResp}
	openRouter := &mockProvider{name: "openrouter", chatResponse: openRouterResp}

	registry := newTestRegistryWithModels(
		registryModelEntry{
			provider:     openAI,
			providerName: "openai_test",
			providerType: "openai",
			modelID:      "gpt-5-nano",
		},
		registryModelEntry{
			provider:     openRouter,
			providerName: "openrouter",
			providerType: "openrouter",
			modelID:      "openai/gpt-5-nano",
		},
	)

	router, _ := NewRouter(registry)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "openai/gpt-5-nano"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "openai-test" {
		t.Fatalf("expected openai_test response, got %q", resp.ID)
	}
	if openAI.lastChatReq == nil || openAI.lastChatReq.Model != "gpt-5-nano" {
		t.Fatalf("expected openai provider to receive raw model gpt-5-nano, got %#v", openAI.lastChatReq)
	}
	if openAI.lastChatReq.Provider != "" {
		t.Fatalf("expected provider field to be stripped upstream, got %q", openAI.lastChatReq.Provider)
	}
	if openRouter.lastChatReq != nil {
		t.Fatalf("expected openrouter provider to be bypassed, got %#v", openRouter.lastChatReq)
	}
	if got := router.GetProviderType("openai/gpt-5-nano"); got != "openai" {
		t.Fatalf("GetProviderType() = %q, want %q", got, "openai")
	}
	if got := router.GetProviderName("openai/gpt-5-nano"); got != "openai_test" {
		t.Fatalf("GetProviderName() = %q, want %q", got, "openai_test")
	}
}

func TestRouterChatCompletion_ProviderQualifiedRawSlashModelStillWorks(t *testing.T) {
	openAIResp := &core.ChatResponse{ID: "openai-test", Model: "gpt-5-nano"}
	openRouterResp := &core.ChatResponse{ID: "openrouter", Model: "openai/gpt-5-nano"}
	openAI := &mockProvider{name: "openai_test", chatResponse: openAIResp}
	openRouter := &mockProvider{name: "openrouter", chatResponse: openRouterResp}

	registry := newTestRegistryWithModels(
		registryModelEntry{
			provider:     openAI,
			providerName: "openai_test",
			providerType: "openai",
			modelID:      "gpt-5-nano",
		},
		registryModelEntry{
			provider:     openRouter,
			providerName: "openrouter",
			providerType: "openrouter",
			modelID:      "openai/gpt-5-nano",
		},
	)

	router, _ := NewRouter(registry)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "openrouter/openai/gpt-5-nano"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Fatalf("expected openrouter response, got %q", resp.ID)
	}
	if openRouter.lastChatReq == nil || openRouter.lastChatReq.Model != "openai/gpt-5-nano" {
		t.Fatalf("expected openrouter provider to receive raw slash model, got %#v", openRouter.lastChatReq)
	}
	if openRouter.lastChatReq.Provider != "" {
		t.Fatalf("expected provider field to be stripped upstream, got %q", openRouter.lastChatReq.Provider)
	}
}

func TestRouterChatCompletion_ProviderOwnedRawSlashModelStillWorks(t *testing.T) {
	openRouterResp := &core.ChatResponse{ID: "openrouter", Model: "openrouter/free"}
	openRouter := &mockProvider{name: "openrouter", chatResponse: openRouterResp}

	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     openRouter,
		providerName: "openrouter",
		providerType: "openrouter",
		modelID:      "openrouter/free",
	})

	router, _ := NewRouter(registry)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "openrouter/free"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Fatalf("expected openrouter response, got %q", resp.ID)
	}
	if openRouter.lastChatReq == nil || openRouter.lastChatReq.Model != "openrouter/free" {
		t.Fatalf("expected openrouter provider to receive raw slash model, got %#v", openRouter.lastChatReq)
	}
	if openRouter.lastChatReq.Provider != "" {
		t.Fatalf("expected provider field to be stripped upstream, got %q", openRouter.lastChatReq.Provider)
	}
	if got := router.GetProviderType("openrouter/free"); got != "openrouter" {
		t.Fatalf("GetProviderType() = %q, want openrouter", got)
	}
	if got := router.GetProviderName("openrouter/free"); got != "openrouter" {
		t.Fatalf("GetProviderName() = %q, want openrouter", got)
	}
}

func TestRouterChatCompletion_ProviderTypeOwnedRawSlashModelStillWorks(t *testing.T) {
	openRouterResp := &core.ChatResponse{ID: "openrouter", Model: "openrouter/auto"}
	openRouter := &mockProvider{name: "openrouter-main", chatResponse: openRouterResp}

	registry := newTestRegistryWithModels(registryModelEntry{
		provider:     openRouter,
		providerName: "openrouter-main",
		providerType: "openrouter",
		modelID:      "openrouter/auto",
	})

	router, _ := NewRouter(registry)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "openrouter/auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Fatalf("expected openrouter response, got %q", resp.ID)
	}
	if openRouter.lastChatReq == nil || openRouter.lastChatReq.Model != "openrouter/auto" {
		t.Fatalf("expected openrouter provider to receive raw slash model, got %#v", openRouter.lastChatReq)
	}
	if openRouter.lastChatReq.Provider != "" {
		t.Fatalf("expected provider field to be stripped upstream, got %q", openRouter.lastChatReq.Provider)
	}
	if got := router.GetProviderName("openrouter/auto"); got != "openrouter-main" {
		t.Fatalf("GetProviderName() = %q, want openrouter-main", got)
	}
}

func TestRouterChatCompletion_ExplicitProviderKeepsSlashModelRaw(t *testing.T) {
	groqResp := &core.ChatResponse{ID: "groq", Model: "openai/gpt-oss-120b"}
	groq := &mockProvider{name: "groq", chatResponse: groqResp}

	lookup := newMockLookup()
	lookup.addModel("groq/openai/gpt-oss-120b", groq, "groq")

	router, _ := NewRouter(lookup)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "openai/gpt-oss-120b",
		Provider: "groq",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "groq" {
		t.Fatalf("expected groq provider response, got %q", resp.ID)
	}
	if groq.lastChatReq == nil || groq.lastChatReq.Model != "openai/gpt-oss-120b" {
		t.Fatalf("expected upstream model to keep raw slash ID, got %#v", groq.lastChatReq)
	}
	if groq.lastChatReq.Provider != "" {
		t.Fatalf("expected provider field to be stripped upstream, got %q", groq.lastChatReq.Provider)
	}
}

func TestRouterResponses(t *testing.T) {
	expectedResp := &core.ResponsesResponse{ID: "resp-123"}
	provider := &mockProvider{name: "openai", responsesResponse: expectedResp}
	altResp := &core.ResponsesResponse{ID: "resp-456"}
	altProvider := &mockProvider{name: "openai-alt", responsesResponse: altResp}

	lookup := newMockLookup()
	lookup.addModel("gpt-4o", provider, "openai")
	lookup.addModel("openai-alt/gpt-4o", altProvider, "openai")

	router, _ := NewRouter(lookup)

	t.Run("routes correctly and stamps provider", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "gpt-4o"}
		resp, err := router.Responses(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if resp.ID != expectedResp.ID {
			t.Errorf("got ID %q, want %q", resp.ID, expectedResp.ID)
		}
		if resp.Provider != "openai" {
			t.Errorf("Provider = %q, want %q", resp.Provider, "openai")
		}
	})

	t.Run("unknown model returns error", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "unknown"}
		_, err := router.Responses(context.Background(), req)
		if err == nil {
			t.Error("expected error for unknown model")
		}
	})

	t.Run("provider selector routes and strips provider before upstream", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "gpt-4o", Provider: "openai-alt"}
		resp, err := router.Responses(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.ID != altResp.ID {
			t.Fatalf("got ID %q, want %q", resp.ID, altResp.ID)
		}
		if altProvider.lastResponsesReq == nil || altProvider.lastResponsesReq.Model != "gpt-4o" {
			t.Fatalf("expected upstream model gpt-4o, got %#v", altProvider.lastResponsesReq)
		}
		if altProvider.lastResponsesReq.Provider != "" {
			t.Fatalf("expected provider field stripped upstream, got %q", altProvider.lastResponsesReq.Provider)
		}
	})
}

func TestRouterResponseUtilitiesStripProviderHint(t *testing.T) {
	provider := &mockResponseProvider{}
	lookup := newTestRegistryWithModels(registryModelEntry{
		provider:     provider,
		providerName: "openai_primary",
		providerType: "openai",
		modelID:      "gpt-4o",
	})
	router, _ := NewRouter(lookup)

	req := &core.ResponsesRequest{
		Model:    "gpt-4o",
		Provider: "openai_primary",
		Input:    "hello",
	}
	_, err := router.CountResponseInputTokens(context.Background(), "openai", req)
	if err != nil {
		t.Fatalf("CountResponseInputTokens() error = %v", err)
	}
	if provider.lastInputTokensReq == nil {
		t.Fatal("expected input token request to be captured")
	}
	if provider.lastInputTokensReq.Provider != "" {
		t.Fatalf("upstream provider hint = %q, want empty", provider.lastInputTokensReq.Provider)
	}
	if req.Provider != "openai_primary" {
		t.Fatalf("original request provider mutated to %q", req.Provider)
	}

	_, err = router.CompactResponse(context.Background(), "openai", req)
	if err != nil {
		t.Fatalf("CompactResponse() error = %v", err)
	}
	if provider.lastCompactReq == nil {
		t.Fatal("expected compact request to be captured")
	}
	if provider.lastCompactReq.Provider != "" {
		t.Fatalf("upstream provider hint = %q, want empty", provider.lastCompactReq.Provider)
	}
}

func TestRouterResponseLifecycleRoutesByProviderName(t *testing.T) {
	primary := &mockResponseProvider{}
	backup := &mockResponseProvider{}
	lookup := newTestRegistryWithModels(
		registryModelEntry{
			provider:     primary,
			providerName: "openai_primary",
			providerType: "openai",
			modelID:      "gpt-4o",
		},
		registryModelEntry{
			provider:     backup,
			providerName: "openai_backup",
			providerType: "openai",
			modelID:      "gpt-4o",
		},
	)
	router, _ := NewRouter(lookup)

	resp, err := router.CancelResponse(context.Background(), "openai_backup", "resp_1")
	if err != nil {
		t.Fatalf("CancelResponse() error = %v", err)
	}
	if backup.cancelledResponse != "resp_1" {
		t.Fatalf("backup cancelled response = %q, want resp_1", backup.cancelledResponse)
	}
	if primary.cancelledResponse != "" {
		t.Fatalf("primary cancelled response = %q, want empty", primary.cancelledResponse)
	}
	if resp.Provider != "openai" {
		t.Fatalf("response provider = %q, want openai", resp.Provider)
	}
}

func TestRouterListModels(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("gpt-4o", &mockProvider{}, "openai")
	lookup.setPublicModels([]core.Model{
		{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		{ID: "openrouter/gpt-4o", Object: "model", OwnedBy: "openrouter"},
		{ID: "azure-openai/gpt-4o", Object: "model", OwnedBy: "azure-openai"},
	})

	router, _ := NewRouter(lookup)

	resp, err := router.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Data) != 3 {
		t.Errorf("expected 3 models, got %d", len(resp.Data))
	}
	if resp.Object != "list" {
		t.Errorf("expected object 'list', got %q", resp.Object)
	}
	if lookup.listCalls != 0 {
		t.Fatalf("ListModels() called %d times, want 0 when publicModelLister is available", lookup.listCalls)
	}
	if lookup.publicCalls != 1 {
		t.Fatalf("ListPublicModels() called %d times, want 1", lookup.publicCalls)
	}
	want := []core.Model{
		{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		{ID: "openrouter/gpt-4o", Object: "model", OwnedBy: "openrouter"},
		{ID: "azure-openai/gpt-4o", Object: "model", OwnedBy: "azure-openai"},
	}
	for i, model := range want {
		if resp.Data[i].ID != model.ID {
			t.Fatalf("resp.Data[%d].ID = %q, want %q", i, resp.Data[i].ID, model.ID)
		}
		if resp.Data[i].OwnedBy != model.OwnedBy {
			t.Fatalf("resp.Data[%d].OwnedBy = %q, want %q", i, resp.Data[i].OwnedBy, model.OwnedBy)
		}
	}
}

func TestRouterGetProviderType(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("gpt-4o", &mockProvider{}, "openai")
	lookup.addModel("claude-3-5-sonnet", &mockProvider{}, "anthropic")

	router, _ := NewRouter(lookup)

	tests := []struct {
		model    string
		expected string
	}{
		{"gpt-4o", "openai"},
		{"claude-3-5-sonnet", "anthropic"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := router.GetProviderType(tt.model); got != tt.expected {
				t.Errorf("GetProviderType(%q) = %q, want %q", tt.model, got, tt.expected)
			}
		})
	}
}

func TestRouterBatchProviderTypeValidation(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("gpt-4o", &mockBatchProvider{}, "openai")

	router, _ := NewRouter(lookup)

	tests := []struct {
		name         string
		providerType string
		call         func() error
	}{
		{
			name:         "empty provider type",
			providerType: "",
			call: func() error {
				_, err := router.GetBatch(context.Background(), "", "batch_1")
				return err
			},
		},
		{
			name:         "unknown provider type",
			providerType: "does-not-exist",
			call: func() error {
				_, err := router.GetBatch(context.Background(), "does-not-exist", "batch_1")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("expected error")
			}
			var gwErr *core.GatewayError
			if !errors.As(err, &gwErr) {
				t.Fatalf("expected GatewayError, got %T: %v", err, err)
			}
			if gwErr.HTTPStatusCode() != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d", http.StatusBadRequest, gwErr.HTTPStatusCode())
			}
		})
	}
}

func TestRouterFileProviderTypeValidation(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("gpt-4o", &mockBatchProvider{}, "openai")

	router, _ := NewRouter(lookup)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "empty provider type",
			call: func() error {
				_, err := router.GetFile(context.Background(), "", "file_1")
				return err
			},
		},
		{
			name: "unknown provider type",
			call: func() error {
				_, err := router.GetFile(context.Background(), "does-not-exist", "file_1")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatal("expected error")
			}
			var gwErr *core.GatewayError
			if !errors.As(err, &gwErr) {
				t.Fatalf("expected GatewayError, got %T: %v", err, err)
			}
			if gwErr.HTTPStatusCode() != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d", gwErr.HTTPStatusCode())
			}
		})
	}
}

func TestRouterListBatchesSetsProviderOnItems(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("gpt-4o", &mockBatchProvider{
		listBatchesResp: &core.BatchListResponse{
			Object: "list",
			Data: []core.BatchResponse{
				{ID: "batch_1", Object: "batch"},
			},
		},
	}, "openai")

	router, _ := NewRouter(lookup)

	resp, err := router.ListBatches(context.Background(), "openai", 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Data))
	}
	if resp.Data[0].Provider != "openai" {
		t.Fatalf("expected provider=openai, got %q", resp.Data[0].Provider)
	}
}

func TestRouterGetBatchResultsWithHintsUsesHintAwareProvider(t *testing.T) {
	provider := &mockBatchProvider{
		hintedBatchResults: &core.BatchResultsResponse{
			Object:  "list",
			BatchID: "provider-batch-1",
			Data: []core.BatchResultItem{
				{Index: 0, URL: "/v1/responses"},
			},
		},
	}
	lookup := newMockLookup()
	lookup.addModel("claude-sonnet", provider, "anthropic")

	router, _ := NewRouter(lookup)
	resp, err := router.GetBatchResultsWithHints(context.Background(), "anthropic", "provider-batch-1", map[string]string{
		"resp-1": "/v1/responses",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || len(resp.Data) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := provider.capturedBatchHints["resp-1"]; got != "/v1/responses" {
		t.Fatalf("capturedBatchHints[resp-1] = %q, want /v1/responses", got)
	}
	if provider.capturedBatchID != "provider-batch-1" {
		t.Fatalf("capturedBatchID = %q, want provider-batch-1", provider.capturedBatchID)
	}

	router.ClearBatchResultHints("anthropic", "provider-batch-1")
	if provider.clearedBatchHintID != "provider-batch-1" {
		t.Fatalf("clearedBatchHintID = %q, want provider-batch-1", provider.clearedBatchHintID)
	}
}

func TestRouterEmbeddings(t *testing.T) {
	expectedResp := &core.EmbeddingResponse{
		Object:   "list",
		Model:    "text-embedding-3-small",
		Provider: "openai",
		Data: []core.EmbeddingData{
			{Object: "embedding", Embedding: json.RawMessage(`[0.1,0.2]`), Index: 0},
		},
	}
	provider := &mockProvider{name: "openai", embeddingResponse: expectedResp}
	altProvider := &mockProvider{name: "openai-alt", embeddingResponse: expectedResp}

	lookup := newMockLookup()
	lookup.addModel("text-embedding-3-small", provider, "openai")
	lookup.addModel("openai-alt/text-embedding-3-small", altProvider, "openai")

	router, _ := NewRouter(lookup)

	t.Run("routes correctly and stamps provider", func(t *testing.T) {
		req := &core.EmbeddingRequest{Model: "text-embedding-3-small", Input: "hello"}
		resp, err := router.Embeddings(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if resp.Model != expectedResp.Model {
			t.Errorf("got Model %q, want %q", resp.Model, expectedResp.Model)
		}
		if resp.Provider != "openai" {
			t.Errorf("Provider = %q, want %q", resp.Provider, "openai")
		}
	})

	t.Run("unknown model returns error", func(t *testing.T) {
		req := &core.EmbeddingRequest{Model: "unknown"}
		_, err := router.Embeddings(context.Background(), req)
		if err == nil {
			t.Error("expected error for unknown model")
		}
	})

	t.Run("provider selector routes and strips provider before upstream", func(t *testing.T) {
		req := &core.EmbeddingRequest{
			Model:    "text-embedding-3-small",
			Provider: "openai-alt",
			Input:    "hello",
		}
		_, err := router.Embeddings(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if altProvider.lastEmbeddingReq == nil || altProvider.lastEmbeddingReq.Model != "text-embedding-3-small" {
			t.Fatalf("expected upstream model text-embedding-3-small, got %#v", altProvider.lastEmbeddingReq)
		}
		if altProvider.lastEmbeddingReq.Provider != "" {
			t.Fatalf("expected provider field stripped upstream, got %q", altProvider.lastEmbeddingReq.Provider)
		}
	})
}

func TestRouterEmbeddings_EmptyLookup(t *testing.T) {
	lookup := newMockLookup()
	router, _ := NewRouter(lookup)

	_, err := router.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "any"})
	if !errors.Is(err, ErrRegistryNotInitialized) {
		t.Errorf("expected ErrRegistryNotInitialized, got: %v", err)
	}
	var gwErr *core.GatewayError
	if !errors.As(err, &gwErr) {
		t.Fatalf("expected GatewayError, got %T: %v", err, err)
	}
	if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
	}
}

func TestRouterEmbeddings_ProviderError(t *testing.T) {
	providerErr := core.NewInvalidRequestError("anthropic does not support embeddings", nil)
	provider := &mockProvider{name: "anthropic", err: providerErr}

	lookup := newMockLookup()
	lookup.addModel("claude-3-5-sonnet", provider, "anthropic")

	router, _ := NewRouter(lookup)

	req := &core.EmbeddingRequest{Model: "claude-3-5-sonnet"}
	_, err := router.Embeddings(context.Background(), req)
	if err == nil {
		t.Error("expected error from provider")
	}
	if _, ok := errors.AsType[*core.GatewayError](err); !ok {
		t.Errorf("expected GatewayError, got %T: %v", err, err)
	}
}

func TestRouterProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	provider := &mockProvider{name: "failing", err: providerErr}

	lookup := newMockLookup()
	lookup.addModel("failing-model", provider, "test")

	router, _ := NewRouter(lookup)

	t.Run("ChatCompletion propagates error", func(t *testing.T) {
		req := &core.ChatRequest{Model: "failing-model"}
		_, err := router.ChatCompletion(context.Background(), req)
		if !errors.Is(err, providerErr) {
			t.Errorf("expected provider error, got: %v", err)
		}
	})

	t.Run("Responses propagates error", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "failing-model"}
		_, err := router.Responses(context.Background(), req)
		if !errors.Is(err, providerErr) {
			t.Errorf("expected provider error, got: %v", err)
		}
	})
}

func TestRouterPassthrough(t *testing.T) {
	provider := &mockProvider{name: "openai"}
	lookup := newMockLookup()
	lookup.addModel("gpt-5-mini", provider, "openai")

	router, _ := NewRouter(lookup)

	resp, err := router.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "responses",
		Body:     io.NopCloser(strings.NewReader(`{"model":"gpt-5-mini"}`)),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.lastPassthrough == nil {
		t.Fatal("provider did not receive passthrough request")
	}
	if provider.lastPassthrough.Endpoint != "responses" {
		t.Fatalf("endpoint = %q, want responses", provider.lastPassthrough.Endpoint)
	}
	if got := readAndCloseBody(t, provider.lastPassthrough.Body); got != `{"model":"gpt-5-mini"}` {
		t.Fatalf("body = %q", got)
	}
	if got := provider.lastPassthrough.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q", string(body))
	}
}

func TestRouterPassthrough_ErrorCases(t *testing.T) {
	t.Run("unknown provider returns gateway error", func(t *testing.T) {
		lookup := newMockLookup()
		lookup.addModel("gpt-5-mini", &mockProvider{name: "openai"}, "openai")
		router, _ := NewRouter(lookup)

		_, err := router.Passthrough(context.Background(), "does-not-exist", &core.PassthroughRequest{
			Method:   http.MethodGet,
			Endpoint: "responses",
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if _, ok := errors.AsType[*core.GatewayError](err); !ok {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
	})

	t.Run("provider error is propagated", func(t *testing.T) {
		providerErr := errors.New("provider passthrough error")
		provider := &mockProvider{name: "openai", err: providerErr}
		lookup := newMockLookup()
		lookup.addModel("gpt-5-mini", provider, "openai")
		router, _ := NewRouter(lookup)

		_, err := router.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
			Method:   http.MethodGet,
			Endpoint: "responses",
		})
		if !errors.Is(err, providerErr) {
			t.Fatalf("expected provider error, got %v", err)
		}
	})

	t.Run("empty registry returns not initialized", func(t *testing.T) {
		router, _ := NewRouter(newMockLookup())

		_, err := router.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
			Method:   http.MethodGet,
			Endpoint: "responses",
		})
		if !errors.Is(err, ErrRegistryNotInitialized) {
			t.Fatalf("expected ErrRegistryNotInitialized, got %v", err)
		}
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
			t.Fatalf("expected GatewayError, got %T: %v", err, err)
		}
		if gwErr.HTTPStatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 status, got %d", gwErr.HTTPStatusCode())
		}
	})
}

func TestRouterPassthrough_UsesProviderRegistryWithoutModels(t *testing.T) {
	provider := &mockProvider{name: "openai"}
	registry := NewModelRegistry()
	registry.RegisterProviderWithType(provider, "openai")
	registry.initialized = true

	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := router.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "models",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if provider.lastPassthrough == nil {
		t.Fatal("provider did not receive passthrough request")
	}
}

func TestRouterListModelsUnqualifiedIDs(t *testing.T) {
	registry := newTestRegistryWithModels(
		registryModelEntry{provider: &mockProvider{}, providerName: "openai", providerType: "openai", modelID: "gpt-5"},
		registryModelEntry{provider: &mockProvider{}, providerName: "azure", providerType: "azure", modelID: "gpt-5"},
		registryModelEntry{provider: &mockProvider{}, providerName: "anthropic", providerType: "anthropic", modelID: "claude-sonnet-4-6"},
	)
	router, err := NewRouter(registry)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	resp, err := router.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("expected 3 qualified models by default, got %d", len(resp.Data))
	}

	router.SetUnqualifiedModelIDs(true)
	resp, err = router.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 deduplicated models, got %d: %+v", len(resp.Data), resp.Data)
	}
	for _, m := range resp.Data {
		if strings.Contains(m.ID, "/") {
			t.Errorf("expected bare model ID, got %q", m.ID)
		}
	}
	if resp.Data[0].ID != "claude-sonnet-4-6" || resp.Data[0].OwnedBy != "anthropic" {
		t.Errorf("unexpected first entry: %+v", resp.Data[0])
	}
	// The listed owner must be the provider an unqualified request routes to.
	want := registry.GetProviderName("gpt-5")
	if resp.Data[1].ID != "gpt-5" || resp.Data[1].OwnedBy != want {
		t.Errorf("expected gpt-5 owned by routing winner %q, got %+v", want, resp.Data[1])
	}
}

func TestAdaptAnthropicBatchCacheControl_StripsAnthropicOnlyMessageFieldsForOpenRouter(t *testing.T) {
	body := `{"model":"claude-sonnet-4-5","messages":[
		{"role":"assistant","content":"x","extra_content":{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"t","signature":"s"}]}},"cache_control":{"type":"ephemeral"}},
		{"role":"tool","tool_call_id":"c1","content":"boom","extra_content":{"anthropic":{"is_error":true}}}
	]}`
	request := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{{
			CustomID: "item-1",
			Method:   http.MethodPost,
			URL:      "/v1/chat/completions",
			Body:     json.RawMessage(body),
		}},
	}
	ctx := core.WithRequestDialect(context.Background(), core.RequestDialectAnthropicMessages)

	if got, err := adaptAnthropicBatchCacheControl(ctx, request, "anthropic"); err != nil || got != request {
		t.Fatalf("anthropic batch = %#v, %v; want the request untouched", got, err)
	}

	adapted, err := adaptAnthropicBatchCacheControl(ctx, request, "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := core.DecodeKnownBatchItemRequest(adapted.Endpoint, adapted.Requests[0])
	if err != nil {
		t.Fatal(err)
	}
	chat := decoded.Request.(*core.ChatRequest)
	if raw := chat.Messages[0].ExtraFields.Lookup(core.ExtraContentField); len(raw) != 0 {
		t.Errorf("openrouter batch kept thinking_blocks: %s", raw)
	}
	if raw := chat.Messages[1].ExtraFields.Lookup(core.ExtraContentField); len(raw) != 0 {
		t.Errorf("openrouter batch kept is_error: %s", raw)
	}
	if got := string(chat.Messages[0].ExtraFields.Lookup("cache_control")); got != `{"type":"ephemeral"}` {
		t.Errorf("openrouter batch lost cache_control: %s", got)
	}
}
