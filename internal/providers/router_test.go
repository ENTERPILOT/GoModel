package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"gomodel/internal/core"
)

// mockModelLookup implements core.ModelLookup for fast, isolated Router testing.
// This is simpler and faster than using a full ModelRegistry with providers.
type mockModelLookup struct {
	models        map[string]core.Provider
	providerTypes map[string]string
	modelList     []core.Model
}

func newMockLookup() *mockModelLookup {
	return &mockModelLookup{
		models:        make(map[string]core.Provider),
		providerTypes: make(map[string]string),
	}
}

func (m *mockModelLookup) addModel(model string, provider core.Provider, providerType string) {
	m.models[model] = provider
	m.providerTypes[model] = providerType
	m.modelList = append(m.modelList, core.Model{ID: model, Object: "model"})
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
	return m.modelList
}

func (m *mockModelLookup) ModelCount() int {
	return len(m.models)
}

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

type mockBatchProvider struct {
	mockProvider
	listBatchesResp    *core.BatchListResponse
	hintedBatchResults *core.BatchResultsResponse
	capturedBatchHints map[string]string
	capturedBatchID    string
	clearedBatchHintID string
}

func (m *mockBatchProvider) CreateBatch(_ context.Context, _ *core.BatchRequest) (*core.BatchResponse, error) {
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
		for customID, endpoint := range endpointByCustomID {
			m.capturedBatchHints[customID] = endpoint
		}
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

func TestRouterChatCompletion_ProviderConflict(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("openai/gpt-4o", &mockProvider{}, "openai")

	router, _ := NewRouter(lookup)

	_, err := router.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "openai/gpt-4o",
		Provider: "anthropic",
	})
	if err == nil {
		t.Fatal("expected provider conflict error")
	}
	var gwErr *core.GatewayError
	if !errors.As(err, &gwErr) {
		t.Fatalf("expected GatewayError, got %T: %v", err, err)
	}
	if gwErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("expected 400 status, got %d", gwErr.HTTPStatusCode())
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

func TestRouterListModels(t *testing.T) {
	lookup := newMockLookup()
	lookup.addModel("gpt-4o", &mockProvider{}, "openai")
	lookup.addModel("claude-3-5-sonnet", &mockProvider{}, "anthropic")

	router, _ := NewRouter(lookup)

	resp, err := router.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Errorf("expected 2 models, got %d", len(resp.Data))
	}
	if resp.Object != "list" {
		t.Errorf("expected object 'list', got %q", resp.Object)
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
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
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
		var gwErr *core.GatewayError
		if !errors.As(err, &gwErr) {
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
