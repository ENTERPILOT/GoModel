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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

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
		assert.Error(t, err)
		assert.Nil(t, router)
	})

	t.Run("valid lookup succeeds", func(t *testing.T) {
		lookup := newMockLookup()
		router, err := NewRouter(lookup)
		assert.NoError(t, err)
		assert.NotNil(t, router)
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
	require.NoError(t, err)
	assert.Equal(t, `{"text":"hello"}`, string(resp.Data), "response = %s", resp.Data)
	require.NotNil(t, translator.lastTranslationReq)
	assert.Equal(t, "whisper-1", translator.lastTranslationReq.Model)
	assert.Empty(t, translator.lastTranslationReq.Provider)
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
			require.NoError(t, err)

			_, err = router.CreateTranslation(context.Background(), tt.req)
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)

				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestRouterEmptyLookup(t *testing.T) {
	lookup := newMockLookup() // Empty - no models
	router, _ := NewRouter(lookup)

	t.Run("Supports returns false", func(t *testing.T) {
		assert.False(t, router.Supports("any-model"))
	})

	t.Run("ChatCompletion returns error", func(t *testing.T) {
		_, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "any"})
		assert.ErrorIs(t, err, ErrRegistryNotInitialized)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
	})

	t.Run("StreamChatCompletion returns error", func(t *testing.T) {
		_, err := router.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "any"})
		assert.ErrorIs(t, err, ErrRegistryNotInitialized)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
	})

	t.Run("ListModels returns error", func(t *testing.T) {
		_, err := router.ListModels(context.Background())
		assert.ErrorIs(t, err, ErrRegistryNotInitialized)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
	})

	t.Run("Responses returns error", func(t *testing.T) {
		_, err := router.Responses(context.Background(), &core.ResponsesRequest{Model: "any"})
		assert.ErrorIs(t, err, ErrRegistryNotInitialized)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
	})

	t.Run("StreamResponses returns error", func(t *testing.T) {
		_, err := router.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "any"})
		assert.ErrorIs(t, err, ErrRegistryNotInitialized)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
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
			got := router.Supports(tt.model)
			assert.Equal(t, tt.expected, got, "Supports(%q)", tt.model)
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
				var gwErr *core.GatewayError
				require.ErrorAs(t, err, &gwErr)
				require.Equal(t, http.StatusNotFound, gwErr.HTTPStatusCode())

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantResp.ID, resp.ID)
			assert.Equal(t, tt.wantProvider, resp.Provider)
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
			_, ok := lookup.(modelInfoLookup)
			require.Equal(t, tt.wantInfoPath, ok)

			router, err := NewRouter(lookup)
			require.NoError(t, err)

			req := &core.ChatRequest{Model: tt.model, Provider: tt.providerHint}
			resp, err := router.ChatCompletion(context.Background(), req)
			require.NoError(t, err)
			require.NotNil(t, provider.lastChatReq)
			got := provider.lastChatReq.Model
			require.Equal(t, "gpt-4o", got)
			got = provider.lastChatReq.Provider
			require.Empty(t, got)
			require.Equal(t, "openai", resp.Provider)
			require.Equal(t, tt.model, req.Model)
			require.Equal(t, tt.providerHint, req.Provider, "caller request mutated: %+v", req)
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
	require.NoError(t, err)
	require.Equal(t, "west", resp.ID)
	require.NotNil(t, west.lastChatReq)
	require.Equal(t, "gpt-4o", west.lastChatReq.Model)
	require.Empty(t, west.lastChatReq.Provider)
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
			require.NoError(t, err)

			provider := &mockProvider{name: tt.providerType, chatResponse: &core.ChatResponse{ID: "ok"}}
			lookup := newMockLookup()
			lookup.addModel("gpt-4o", provider, tt.providerType)
			router, _ := NewRouter(lookup)
			ctx := context.Background()
			if tt.messagesIngress {
				ctx = core.WithRequestDialect(ctx, core.RequestDialectAnthropicMessages)
			}
			_, err = router.ChatCompletion(ctx, request)
			require.NoError(t, err)

			forwarded := provider.lastChatReq
			require.NotNil(t, forwarded)

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
				got := string(fields.Lookup("x_keep"))
				assert.Equal(t, "true", got, "%s x_keep should be preserved", label)
			}
			assertFields("request", forwarded.ExtraFields, tt.wantCache)
			toolCache, toolHasCache := forwarded.Tools[0]["cache_control"]
			if tt.wantCache {
				encoded, err := json.Marshal(toolCache)
				require.NoError(t, err)
				assert.True(t, toolHasCache)
				assert.JSONEq(t, string(expectedCache), string(encoded))
			} else {
				assert.False(t, toolHasCache, "tool cache_control = %v, want absent", toolCache)
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
			require.NoError(t, err)
			require.Equal(t, before, after)
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
				_, _, err := router.CreateBatchWithHints(ctx, tt.providerType, request)
				require.NoError(t, err)
			} else {
				_, err := router.CreateBatch(ctx, tt.providerType, request)
				require.NoError(t, err)
			}

			require.NotNil(t, provider.lastBatchReq)
			require.Len(t, provider.lastBatchReq.Requests, 1)

			decoded, err := core.DecodeKnownBatchItemRequest(provider.lastBatchReq.Endpoint, provider.lastBatchReq.Requests[0])
			require.NoError(t, err)

			chat := decoded.Request.(*core.ChatRequest)
			gotCache := chat.ExtraFields.Lookup("cache_control")
			if tt.wantCache {
				assert.JSONEq(t, string(expectedCache), string(gotCache), "request cache_control")
			} else {
				assert.Empty(t, gotCache, "request cache_control should be absent")
			}
			got := string(chat.ExtraFields.Lookup("x_keep"))
			assert.Equal(t, "true", got)

			part := chat.Messages[0].Content.([]core.ContentPart)[0]
			partCache := part.ExtraFields.Lookup("cache_control")
			if tt.wantCache {
				assert.JSONEq(t, string(expectedCache), string(partCache), "content cache_control")
			} else {
				assert.Empty(t, partCache, "content cache_control should be absent")
			}
			got = string(part.ExtraFields.Lookup("x_keep"))
			assert.Equal(t, "true", got)

			toolCache, toolHasCache := chat.Tools[0]["cache_control"]
			if tt.wantCache {
				encoded, err := json.Marshal(toolCache)
				require.NoError(t, err)
				assert.True(t, toolHasCache)
				assert.JSONEq(t, string(expectedCache), string(encoded))
			} else {
				assert.False(t, toolHasCache, "tool cache_control = %v, want absent", toolCache)
			}

			require.Equal(t, before, request.Requests[0].Body)
		})
	}
}

func TestAdaptAnthropicCacheControl_PreservesSupportedProviders(t *testing.T) {
	req := &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
	})}
	for _, providerType := range []string{"anthropic", "openrouter"} {
		got := adaptAnthropicCacheControl(req, providerType)
		assert.Same(t, req, got, "provider %q cloned or changed supported cache metadata", providerType)
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
			require.NotSame(t, req, got)

			call := got.Messages[0].ToolCalls[0]
			for name, fields := range map[string]core.UnknownJSONFields{
				"assistant": got.Messages[0].ExtraFields,
				"tool call": call.ExtraFields,
				"function":  call.Function.ExtraFields,
				"tool":      got.Messages[1].ExtraFields,
			} {
				raw := string(fields.Lookup(core.ExtraContentField))
				assert.Equal(t, tt.want, raw, "%s extra_content", name)
				assert.NotEmpty(t, fields.Lookup("x_keep"), "%s dropped unrelated extra", name)
			}
		})
	}
	raw := req.Messages[0].ToolCalls[0].ExtraFields.ExtraContent(core.ExtraContentVendorAnthropic)
	assert.NotEmpty(t, raw)
}

func TestAdaptExtraContent_ReturnsRequestWithoutExtraContent(t *testing.T) {
	req := &core.ChatRequest{Messages: []core.Message{
		{Role: "assistant", Content: "x", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"x_keep": json.RawMessage("true"),
		}), ToolCalls: []core.ToolCall{{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"}}}},
	}}
	got := adaptExtraContent(req, "openai")
	assert.Same(t, req, got)

	own := &core.ChatRequest{Messages: []core.Message{
		{Role: "assistant", ContentNull: true, ToolCalls: []core.ToolCall{{ID: "c1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"},
			ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"google":{"thought_signature":"sig"}}`),
			})}}},
	}}
	got = adaptExtraContent(own, "gemini")
	assert.Same(t, own, got)
	got = adaptExtraContent(nil, "openai")
	assert.Nil(t, got)
}

func TestAdaptResponsesExtraContent_KeepsOwnVendorOnly(t *testing.T) {
	item := func() map[string]any {
		return map[string]any{
			"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{}",
			"extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig"}, "anthropic": map[string]any{"is_error": true}},
		}
	}
	inputs := map[string]any{
		"any":  []any{"plain string item", item()},
		"maps": []map[string]any{item()},
		"elements": []core.ResponsesInputElement{{Type: "function_call", CallID: "c1", Name: "f", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			core.ExtraContentField: json.RawMessage(`{"google":{"thought_signature":"sig"},"anthropic":{"is_error":true}}`),
		})}},
	}
	extraContentOf := func(t *testing.T, input any) string {
		t.Helper()
		encoded, err := json.Marshal(input)
		require.NoError(t, err)

		var items []any
		err = json.Unmarshal(encoded, &items)
		require.NoError(t, err)

		raw, _ := json.Marshal(items[len(items)-1].(map[string]any)["extra_content"])
		return string(raw)
	}
	for name, input := range inputs {
		for _, tt := range []struct{ providerType, want string }{
			{providerType: "gemini", want: `{"google":{"thought_signature":"sig"}}`},
			{providerType: "anthropic", want: `{"anthropic":{"is_error":true}}`},
			{providerType: "openai", want: `null`},
		} {
			t.Run(name+"/"+tt.providerType, func(t *testing.T) {
				req := &core.ResponsesRequest{Model: "m", Input: input}
				got := adaptResponsesExtraContent(req, tt.providerType)
				require.NotSame(t, req, got)
				raw := extraContentOf(t, got.Input)
				assert.Equal(t, tt.want, raw)
				raw = extraContentOf(t, req.Input)
				assert.NotEqual(t, tt.want, raw)
			})
		}
	}
	for name, input := range map[string]any{
		"string": "hello",
		"clean":  []any{map[string]any{"type": "message", "role": "user", "content": "hi"}},
		"own":    []any{map[string]any{"type": "function_call", "extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig"}}}},
	} {
		req := &core.ResponsesRequest{Model: "m", Input: input}
		got := adaptResponsesExtraContent(req, "gemini")
		assert.Same(t, req, got, "%s input must be returned as-is", name)
	}
}

func TestForwardResponsesRequest_DropsForeignExtraContent(t *testing.T) {
	req := &core.ResponsesRequest{Model: "m", Provider: "p", Input: []any{
		map[string]any{"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{}", "extra_content": map[string]any{"google": map[string]any{"thought_signature": "sig"}}},
	}}
	got := forwardResponsesRequest(req, resolvedRoute{selector: core.ModelSelector{Model: "m"}, providerType: "openai"})
	item := got.Input.([]any)[0].(map[string]any)
	assert.Nil(t, item["extra_content"])
	assert.Empty(t, got.Provider)
	assert.Equal(t, "m", got.Model, "forwarded request = %+v", got)
}

func TestAdaptBatchRequest_StripsForeignExtraContentFromOrdinaryBatches(t *testing.T) {
	chatBody := `{"model":"gpt-4o","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig"}}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	responsesBody := `{"model":"gpt-4o","input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}","extra_content":{"google":{"thought_signature":"sig"}}}]}`
	escapedBody := `{"model":"gpt-4o","messages":[{"role":"tool","tool_call_id":"c1","content":"ok","extra_\u0063ontent":{"anthropic":{"is_error":true}}}]}`
	opaqueBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],  "x": 1}`
	malformedBody := `{"model":"gpt-4o","messages":"extra_content"`
	request := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{CustomID: "chat", Method: http.MethodPost, URL: "/v1/chat/completions", Body: json.RawMessage(chatBody)},
			{CustomID: "responses", Method: http.MethodPost, URL: "/v1/responses", Body: json.RawMessage(responsesBody)},
			{CustomID: "escaped", Method: http.MethodPost, URL: "/v1/chat/completions", Body: json.RawMessage(escapedBody)},
			{CustomID: "opaque", Method: http.MethodPost, URL: "/v1/chat/completions", Body: json.RawMessage(opaqueBody)},
			{CustomID: "malformed", Method: http.MethodPost, URL: "/v1/chat/completions", Body: json.RawMessage(malformedBody)},
		},
	}
	adapted, err := adaptBatchRequest(context.Background(), request, "openai")
	require.NoError(t, err)
	require.NotSame(t, request, adapted)

	for _, i := range []int{0, 1, 2} {
		assert.False(t, bytes.Contains(adapted.Requests[i].Body, []byte("extra_content")))
		assert.False(t, bytes.Contains(adapted.Requests[i].Body, []byte("is_error")), "requests[%d] kept foreign extra_content: %s", i, adapted.Requests[i].Body)
	}
	assert.Equal(t, opaqueBody, string(adapted.Requests[3].Body), "opaque item was rewritten: %s", adapted.Requests[3].Body)
	assert.Equal(t, malformedBody, string(adapted.Requests[4].Body), "undecodable item was rewritten: %s", adapted.Requests[4].Body)
	assert.Contains(t, string(request.Requests[0].Body), string([]byte("thought_signature")))

	own := &core.BatchRequest{Endpoint: request.Endpoint, Requests: []core.BatchRequestItem{request.Requests[0], request.Requests[1], request.Requests[3], request.Requests[4]}}
	got, err := adaptBatchRequest(context.Background(), own, "gemini")
	assert.NoError(t, err)
	assert.Same(t, own, got)
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
		raw := got.Messages[0].ToolCalls[0].ExtraFields.Lookup(core.ExtraContentField)
		assert.Empty(t, raw)
		assert.Empty(t, got.Provider)
	}
	route.providerType = "gemini"
	got := forwardChatRequest(context.Background(), req, route)
	raw := got.Messages[0].ToolCalls[0].ExtraFields.ExtraContent(core.ExtraContentVendorGoogle)
	assert.NotEmpty(t, raw)
}

func TestRouterChatCompletion_PrefixedModelSelector(t *testing.T) {
	westResp := &core.ChatResponse{ID: "west", Model: "gpt-4o"}
	west := &mockProvider{name: "openai-west", chatResponse: westResp}

	lookup := newMockLookup()
	lookup.addModel("openai-west/gpt-4o", west, "openai")

	router, _ := NewRouter(lookup)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "openai-west/gpt-4o"})
	require.NoError(t, err)
	require.Equal(t, "west", resp.ID)
	require.NotNil(t, west.lastChatReq)
	require.Equal(t, "gpt-4o", west.lastChatReq.Model)
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
	require.NoError(t, err)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/later-model"})
	require.NoError(t, err)
	require.Equal(t, "chatcmpl-later", resp.ID)
	require.Equal(t, 1, provider.availabilityCalls)
	require.Equal(t, 1, provider.listModelsCalls)
	require.NotNil(t, provider.lastChatReq)
	require.Equal(t, "later-model", provider.lastChatReq.Model)
	require.True(t, registry.Supports("ollama/later-model"))
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
	require.NoError(t, err)

	resp, err := router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/local-model"})
	require.NoError(t, err)
	require.Equal(t, "ollama", resp.ID)
	require.True(t, registry.Supports("openai/gpt-4o"))
	require.True(t, registry.Supports("ollama/local-model"))
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
	require.NoError(t, err)

	_, err = router.ChatCompletion(context.Background(), &core.ChatRequest{Model: "ollama/later-model"})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusServiceUnavailable, gatewayErr.HTTPStatusCode())
	require.Equal(t, 1, provider.availabilityCalls)
	require.Equal(t, 0, provider.listModelsCalls)
	require.Nil(t, provider.lastChatReq)
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
	require.NoError(t, err)
	require.Equal(t, "openai-test", resp.ID)
	require.NotNil(t, openAI.lastChatReq)
	require.Equal(t, "gpt-5-nano", openAI.lastChatReq.Model)
	require.Empty(t, openAI.lastChatReq.Provider)
	require.Nil(t, openRouter.lastChatReq)
	got := router.GetProviderType("openai/gpt-5-nano")
	require.Equal(t, "openai", got)
	got = router.GetProviderName("openai/gpt-5-nano")
	require.Equal(t, "openai_test", got)
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
	require.NoError(t, err)
	require.Equal(t, "openrouter", resp.ID)
	require.NotNil(t, openRouter.lastChatReq)
	require.Equal(t, "openai/gpt-5-nano", openRouter.lastChatReq.Model)
	require.Empty(t, openRouter.lastChatReq.Provider)
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
	require.NoError(t, err)
	require.Equal(t, "openrouter", resp.ID)
	require.NotNil(t, openRouter.lastChatReq)
	require.Equal(t, "openrouter/free", openRouter.lastChatReq.Model)
	require.Empty(t, openRouter.lastChatReq.Provider)
	got := router.GetProviderType("openrouter/free")
	require.Equal(t, "openrouter", got)
	got = router.GetProviderName("openrouter/free")
	require.Equal(t, "openrouter", got)
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
	require.NoError(t, err)
	require.Equal(t, "openrouter", resp.ID)
	require.NotNil(t, openRouter.lastChatReq)
	require.Equal(t, "openrouter/auto", openRouter.lastChatReq.Model)
	require.Empty(t, openRouter.lastChatReq.Provider)
	got := router.GetProviderName("openrouter/auto")
	require.Equal(t, "openrouter-main", got)
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
	require.NoError(t, err)
	require.Equal(t, "groq", resp.ID)
	require.NotNil(t, groq.lastChatReq)
	require.Equal(t, "openai/gpt-oss-120b", groq.lastChatReq.Model)
	require.Empty(t, groq.lastChatReq.Provider)
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
		require.NoError(t, err)
		assert.Equal(t, expectedResp.ID, resp.ID)
		assert.Equal(t, "openai", resp.Provider)
	})

	t.Run("unknown model returns error", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "unknown"}
		_, err := router.Responses(context.Background(), req)
		assert.Error(t, err)
	})

	t.Run("provider selector routes and strips provider before upstream", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "gpt-4o", Provider: "openai-alt"}
		resp, err := router.Responses(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, altResp.ID, resp.ID)
		require.NotNil(t, altProvider.lastResponsesReq)
		require.Equal(t, "gpt-4o", altProvider.lastResponsesReq.Model)
		require.Empty(t, altProvider.lastResponsesReq.Provider)
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
	require.NoError(t, err)
	require.NotNil(t, provider.lastInputTokensReq)
	require.Empty(t, provider.lastInputTokensReq.Provider)
	require.Equal(t, "openai_primary", req.Provider)

	_, err = router.CompactResponse(context.Background(), "openai", req)
	require.NoError(t, err)
	require.NotNil(t, provider.lastCompactReq)
	require.Empty(t, provider.lastCompactReq.Provider)
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
	require.NoError(t, err)
	require.Equal(t, "resp_1", backup.cancelledResponse)
	require.Empty(t, primary.cancelledResponse)
	require.Equal(t, "openai", resp.Provider)
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
	require.NoError(t, err)
	assert.Len(t, resp.Data, 3)
	assert.Equal(t, "list", resp.Object)
	require.Equal(t, 0, lookup.listCalls)
	require.Equal(t, 1, lookup.publicCalls)

	want := []core.Model{
		{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		{ID: "openrouter/gpt-4o", Object: "model", OwnedBy: "openrouter"},
		{ID: "azure-openai/gpt-4o", Object: "model", OwnedBy: "azure-openai"},
	}
	for i, model := range want {
		require.Equal(t, model.ID, resp.Data[i].ID, "resp.Data[%d].ID", i)
		require.Equal(t, model.OwnedBy, resp.Data[i].OwnedBy, "resp.Data[%d].OwnedBy", i)
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
			got := router.GetProviderType(tt.model)
			assert.Equal(t, tt.expected, got, "GetProviderType(%q)", tt.model)
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
			require.Error(t, err)

			var gwErr *core.GatewayError
			require.ErrorAs(t, err, &gwErr)
			require.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode())
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
			require.Error(t, err)

			var gwErr *core.GatewayError
			require.ErrorAs(t, err, &gwErr)
			require.Equal(t, http.StatusBadRequest, gwErr.HTTPStatusCode())
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
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "openai", resp.Data[0].Provider)
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
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	got := provider.capturedBatchHints["resp-1"]
	require.Equal(t, "/v1/responses", got)
	require.Equal(t, "provider-batch-1", provider.capturedBatchID)

	router.ClearBatchResultHints("anthropic", "provider-batch-1")
	require.Equal(t, "provider-batch-1", provider.clearedBatchHintID)
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
		require.NoError(t, err)
		assert.Equal(t, expectedResp.Model, resp.Model)
		assert.Equal(t, "openai", resp.Provider)
	})

	t.Run("unknown model returns error", func(t *testing.T) {
		req := &core.EmbeddingRequest{Model: "unknown"}
		_, err := router.Embeddings(context.Background(), req)
		assert.Error(t, err)
	})

	t.Run("provider selector routes and strips provider before upstream", func(t *testing.T) {
		req := &core.EmbeddingRequest{
			Model:    "text-embedding-3-small",
			Provider: "openai-alt",
			Input:    "hello",
		}
		_, err := router.Embeddings(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, altProvider.lastEmbeddingReq)
		require.Equal(t, "text-embedding-3-small", altProvider.lastEmbeddingReq.Model)
		require.Empty(t, altProvider.lastEmbeddingReq.Provider)
	})
}

func TestRouterEmbeddings_EmptyLookup(t *testing.T) {
	lookup := newMockLookup()
	router, _ := NewRouter(lookup)

	_, err := router.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "any"})
	assert.ErrorIs(t, err, ErrRegistryNotInitialized)

	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
}

func TestRouterEmbeddings_ProviderError(t *testing.T) {
	providerErr := core.NewInvalidRequestError("anthropic does not support embeddings", nil)
	provider := &mockProvider{name: "anthropic", err: providerErr}

	lookup := newMockLookup()
	lookup.addModel("claude-3-5-sonnet", provider, "anthropic")

	router, _ := NewRouter(lookup)

	req := &core.EmbeddingRequest{Model: "claude-3-5-sonnet"}
	_, err := router.Embeddings(context.Background(), req)
	require.Error(t, err)
	_, ok := errors.AsType[*core.GatewayError](err)
	assert.True(t, ok)
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
		assert.ErrorIs(t, err, providerErr)
	})

	t.Run("Responses propagates error", func(t *testing.T) {
		req := &core.ResponsesRequest{Model: "failing-model"}
		_, err := router.Responses(context.Background(), req)
		assert.ErrorIs(t, err, providerErr)
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
	require.NoError(t, err)
	require.NotNil(t, provider.lastPassthrough)
	require.Equal(t, "responses", provider.lastPassthrough.Endpoint)
	got := readAndCloseBody(t, provider.lastPassthrough.Body)
	require.Equal(t, `{"model":"gpt-5-mini"}`, got)
	got = provider.lastPassthrough.Headers.Get("Content-Type")
	require.Equal(t, "application/json", got)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(body))
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
		require.Error(t, err)
		_, ok := errors.AsType[*core.GatewayError](err)
		require.True(t, ok)
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
		require.ErrorIs(t, err, providerErr)
	})

	t.Run("empty registry returns not initialized", func(t *testing.T) {
		router, _ := NewRouter(newMockLookup())

		_, err := router.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
			Method:   http.MethodGet,
			Endpoint: "responses",
		})
		require.ErrorIs(t, err, ErrRegistryNotInitialized)

		var gwErr *core.GatewayError
		require.ErrorAs(t, err, &gwErr)
		require.Equal(t, http.StatusServiceUnavailable, gwErr.HTTPStatusCode())
	})
}

func TestRouterPassthrough_UsesProviderRegistryWithoutModels(t *testing.T) {
	provider := &mockProvider{name: "openai"}
	registry := NewModelRegistry()
	registry.RegisterProviderWithType(provider, "openai")
	registry.initialized = true

	router, err := NewRouter(registry)
	require.NoError(t, err)

	resp, err := router.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "models",
	})
	require.NoError(t, err)

	defer resp.Body.Close()

	require.NotNil(t, provider.lastPassthrough)
}

func TestRouterListModelsUnqualifiedIDs(t *testing.T) {
	registry := newTestRegistryWithModels(
		registryModelEntry{provider: &mockProvider{}, providerName: "openai", providerType: "openai", modelID: "gpt-5"},
		registryModelEntry{provider: &mockProvider{}, providerName: "azure", providerType: "azure", modelID: "gpt-5"},
		registryModelEntry{provider: &mockProvider{}, providerName: "anthropic", providerType: "anthropic", modelID: "claude-sonnet-4-6"},
	)
	router, err := NewRouter(registry)
	require.NoError(t, err)

	resp, err := router.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 3)

	router.SetUnqualifiedModelIDs(true)
	resp, err = router.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 2)

	for _, m := range resp.Data {
		assert.NotContains(t, m.ID, "/", "expected bare model ID")
	}
	assert.Equal(t, "claude-sonnet-4-6", resp.Data[0].ID)
	assert.Equal(t, "anthropic", resp.Data[0].OwnedBy, "unexpected first entry: %+v", resp.Data[0])

	// The listed owner must be the provider an unqualified request routes to.
	want := registry.GetProviderName("gpt-5")
	assert.Equal(t, "gpt-5", resp.Data[1].ID)
	assert.Equal(t, want, resp.Data[1].OwnedBy, "gpt-5 should be owned by the routing winner: %+v", resp.Data[1])
}

func TestAdaptBatchRequest_RejectsNonChatItemsInAnthropicBatches(t *testing.T) {
	ctx := core.WithRequestDialect(context.Background(), core.RequestDialectAnthropicMessages)
	for _, tc := range []struct{ name, url, body string }{
		{name: "responses", url: "/v1/responses", body: `{"model":"claude-sonnet-4-5","input":"hi"}`},
		{name: "embeddings", url: "/v1/embeddings", body: `{"model":"claude-sonnet-4-5","input":"hi"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &core.BatchRequest{
				Endpoint: "/v1/chat/completions",
				Requests: []core.BatchRequestItem{{CustomID: "item-1", Method: http.MethodPost, URL: tc.url, Body: json.RawMessage(tc.body)}},
			}
			for _, providerType := range []string{"anthropic", "openai"} {
				_, err := adaptBatchRequest(ctx, request, providerType)
				assert.ErrorContains(t, err, "not a chat completion")
			}
			_, err := adaptBatchRequest(context.Background(), request, "openai")
			assert.NoError(t, err)
		})
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
	got, err := adaptBatchRequest(ctx, request, "anthropic")
	require.NoError(t, err)
	require.Same(t, request, got)

	adapted, err := adaptBatchRequest(ctx, request, "openrouter")
	require.NoError(t, err)

	decoded, err := core.DecodeKnownBatchItemRequest(adapted.Endpoint, adapted.Requests[0])
	require.NoError(t, err)

	chat := decoded.Request.(*core.ChatRequest)
	raw := chat.Messages[0].ExtraFields.Lookup(core.ExtraContentField)
	assert.Empty(t, raw)
	raw = chat.Messages[1].ExtraFields.Lookup(core.ExtraContentField)
	assert.Empty(t, raw)
	assert.Equal(t, `{"type":"ephemeral"}`, string(chat.Messages[0].ExtraFields.Lookup("cache_control")), "openrouter batch lost cache_control")
}
