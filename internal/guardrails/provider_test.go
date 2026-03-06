package guardrails

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"gomodel/internal/core"
)

// mockRoutableProvider is a test double for core.RoutableProvider.
type mockRoutableProvider struct {
	supportsFn        func(model string) bool
	getProviderTypeFn func(model string) string
	chatReq           *core.ChatRequest
	responsesReq      *core.ResponsesRequest
	batchReq          *core.BatchRequest
}

func (m *mockRoutableProvider) Supports(model string) bool {
	if m.supportsFn != nil {
		return m.supportsFn(model)
	}
	return true
}

func (m *mockRoutableProvider) GetProviderType(model string) string {
	if m.getProviderTypeFn != nil {
		return m.getProviderTypeFn(model)
	}
	return "mock"
}

func (m *mockRoutableProvider) ChatCompletion(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	m.chatReq = req
	return &core.ChatResponse{Model: req.Model}, nil
}

func (m *mockRoutableProvider) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	m.chatReq = req
	return io.NopCloser(strings.NewReader("data: test\n\n")), nil
}

func (m *mockRoutableProvider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	return &core.ModelsResponse{Object: "list"}, nil
}

func (m *mockRoutableProvider) Responses(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	m.responsesReq = req
	return &core.ResponsesResponse{Model: req.Model}, nil
}

func (m *mockRoutableProvider) StreamResponses(_ context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	m.responsesReq = req
	return io.NopCloser(strings.NewReader("data: test\n\n")), nil
}

func (m *mockRoutableProvider) Embeddings(_ context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return &core.EmbeddingResponse{Object: "list", Model: req.Model, Provider: "mock"}, nil
}

func (m *mockRoutableProvider) CreateBatch(_ context.Context, _ string, req *core.BatchRequest) (*core.BatchResponse, error) {
	m.batchReq = req
	return &core.BatchResponse{ID: "batch_1", Object: "batch", Status: "in_progress"}, nil
}

func (m *mockRoutableProvider) GetBatch(_ context.Context, _, _ string) (*core.BatchResponse, error) {
	return &core.BatchResponse{ID: "batch_1", Object: "batch", Status: "completed"}, nil
}

func (m *mockRoutableProvider) ListBatches(_ context.Context, _ string, _ int, _ string) (*core.BatchListResponse, error) {
	return &core.BatchListResponse{Object: "list"}, nil
}

func (m *mockRoutableProvider) CancelBatch(_ context.Context, _, _ string) (*core.BatchResponse, error) {
	return &core.BatchResponse{ID: "batch_1", Object: "batch", Status: "cancelled"}, nil
}

func (m *mockRoutableProvider) GetBatchResults(_ context.Context, _, _ string) (*core.BatchResultsResponse, error) {
	return &core.BatchResultsResponse{Object: "list", BatchID: "batch_1"}, nil
}

// --- Chat adapter integration tests ---

func TestGuardedProvider_ChatCompletion_AppliesGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model:    "gpt-4",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the inner provider received the modified request
	if inner.chatReq == nil {
		t.Fatal("inner provider was not called")
	}
	if len(inner.chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(inner.chatReq.Messages))
	}
	if inner.chatReq.Messages[0].Role != "system" || inner.chatReq.Messages[0].Content != "guardrail system" {
		t.Errorf("expected injected system message, got %+v", inner.chatReq.Messages[0])
	}
}

func TestGuardedProvider_StreamChatCompletion_AppliesGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "override system")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{Role: "system", Content: "original"},
			{Role: "user", Content: "hello"},
		},
	}

	stream, err := guarded.StreamChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if inner.chatReq.Messages[0].Content != "override system" {
		t.Errorf("expected override, got %q", inner.chatReq.Messages[0].Content)
	}
}

func TestGuardedProvider_ChatPreservesFields(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "system")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	temp := 0.7
	maxTok := 100
	req := &core.ChatRequest{
		Model:       "gpt-4",
		Temperature: &temp,
		MaxTokens:   &maxTok,
		Messages:    []core.Message{{Role: "user", Content: "hello"}},
		Stream:      true,
		Reasoning:   &core.Reasoning{Effort: "high"},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	got := inner.chatReq
	if got.Model != "gpt-4" {
		t.Errorf("model not preserved")
	}
	if got.Temperature == nil || *got.Temperature != 0.7 {
		t.Errorf("temperature not preserved")
	}
	if got.MaxTokens == nil || *got.MaxTokens != 100 {
		t.Errorf("max_tokens not preserved")
	}
	if !got.Stream {
		t.Errorf("stream not preserved")
	}
	if got.Reasoning == nil || got.Reasoning.Effort != "high" {
		t.Errorf("reasoning not preserved")
	}
}

func TestGuardedProvider_ChatCompletion_AppliesGuardrailsToTextOnlyContentArray(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "hello"},
				},
			},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.chatReq == nil {
		t.Fatal("inner provider was not called")
	}
	if len(inner.chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(inner.chatReq.Messages))
	}
	if inner.chatReq.Messages[0].Role != "system" || inner.chatReq.Messages[0].Content != "guardrail system" {
		t.Fatalf("expected injected system message, got %+v", inner.chatReq.Messages[0])
	}
	if got := core.ExtractTextContent(inner.chatReq.Messages[1].Content); got != "hello" {
		t.Fatalf("user content = %q, want hello", got)
	}

	parts, ok := req.Messages[0].Content.([]core.ContentPart)
	if !ok || len(parts) != 1 || parts[0].Text != "hello" {
		t.Fatalf("original request content mutated: %#v", req.Messages[0].Content)
	}
}

func TestGuardedProvider_ChatCompletion_PreservesNonTextMultimodalContentWhileApplyingSystemGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "hello"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
				},
			},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.chatReq == nil {
		t.Fatal("inner provider was not called")
	}
	if len(inner.chatReq.Messages) != 2 {
		t.Fatalf("expected guarded multimodal request with injected system message, got %d messages", len(inner.chatReq.Messages))
	}
	if inner.chatReq.Messages[0].Role != "system" || inner.chatReq.Messages[0].Content != "guardrail system" {
		t.Fatalf("expected injected system message, got %+v", inner.chatReq.Messages[0])
	}
	parts, ok := inner.chatReq.Messages[1].Content.([]core.ContentPart)
	if !ok || len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("expected preserved multimodal content, got %#v", inner.chatReq.Messages[1].Content)
	}
}

func TestGuardedProvider_ChatCompletion_MixedMultimodalAndTextPreservesTextRewrites(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	pipeline.Add(&mockGuardrail{
		name: "rewrite-user-text",
		processFn: func(_ context.Context, msgs []Message) ([]Message, error) {
			out := make([]Message, len(msgs))
			copy(out, msgs)
			for i := range out {
				if out[i].Role == "user" {
					out[i].Content = out[i].Content + " [rewritten]"
				}
			}
			return out, nil
		},
	}, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "describe"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
				},
			},
			{Role: "user", Content: "plain text"},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.chatReq == nil {
		t.Fatal("inner provider was not called")
	}
	if len(inner.chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(inner.chatReq.Messages))
	}
	parts, ok := inner.chatReq.Messages[0].Content.([]core.ContentPart)
	if !ok || len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("expected first message multimodal content preserved, got %#v", inner.chatReq.Messages[0].Content)
	}
	if got := core.ExtractTextContent(inner.chatReq.Messages[1].Content); got != "plain text [rewritten]" {
		t.Fatalf("expected rewritten text-only message, got %q", got)
	}
}

// --- Responses adapter integration tests ---

func TestGuardedProvider_Responses_AppliesGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail instructions")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ResponsesRequest{Model: "gpt-4", Input: "hello"}

	_, err := guarded.Responses(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.responsesReq.Instructions != "guardrail instructions" {
		t.Errorf("expected injected instructions, got %q", inner.responsesReq.Instructions)
	}
}

func TestGuardedProvider_StreamResponses_AppliesGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptDecorator, "prefix")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ResponsesRequest{
		Model:        "gpt-4",
		Input:        "hello",
		Instructions: "existing",
	}

	stream, err := guarded.StreamResponses(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if inner.responsesReq.Instructions != "prefix\nexisting" {
		t.Errorf("expected decorated instructions, got %q", inner.responsesReq.Instructions)
	}
}

func TestGuardedProvider_ResponsesPreservesFields(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "system")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	temp := 0.5
	maxTok := 200
	req := &core.ResponsesRequest{
		Model:           "gpt-4",
		Input:           "hello",
		Temperature:     &temp,
		MaxOutputTokens: &maxTok,
		Tools:           []map[string]any{{"type": "function"}},
		Metadata:        map[string]string{"key": "val"},
		Reasoning:       &core.Reasoning{Effort: "medium"},
	}

	_, err := guarded.Responses(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	got := inner.responsesReq
	if got.Model != "gpt-4" {
		t.Errorf("model not preserved")
	}
	if got.Temperature == nil || *got.Temperature != 0.5 {
		t.Errorf("temperature not preserved")
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 200 {
		t.Errorf("max_output_tokens not preserved")
	}
	if got.Input != "hello" {
		t.Errorf("input not preserved")
	}
	if len(got.Tools) != 1 {
		t.Errorf("tools not preserved")
	}
	if got.Metadata["key"] != "val" {
		t.Errorf("metadata not preserved")
	}
	if got.Reasoning == nil || got.Reasoning.Effort != "medium" {
		t.Errorf("reasoning not preserved")
	}
}

func TestGuardedProvider_CreateBatch_DefaultNoBatchGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)
	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/chat/completions",
				Body:   json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`),
			},
		},
	}

	_, err := guarded.CreateBatch(context.Background(), "mock", req)
	if err != nil {
		t.Fatal(err)
	}
	if inner.batchReq == nil || len(inner.batchReq.Requests) != 1 {
		t.Fatalf("expected delegated batch request")
	}
	var chatReq core.ChatRequest
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &chatReq); err != nil {
		t.Fatal(err)
	}
	if len(chatReq.Messages) != 1 || chatReq.Messages[0].Role != "user" {
		t.Fatalf("expected unchanged batch request when disabled, got: %+v", chatReq.Messages)
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/chat/completions",
				Body:   json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`),
			},
		},
	}

	_, err := guarded.CreateBatch(context.Background(), "mock", req)
	if err != nil {
		t.Fatal(err)
	}
	if inner.batchReq == nil || len(inner.batchReq.Requests) != 1 {
		t.Fatalf("expected delegated batch request")
	}
	var chatReq core.ChatRequest
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &chatReq); err != nil {
		t.Fatal(err)
	}
	if len(chatReq.Messages) != 2 || chatReq.Messages[0].Role != "system" {
		t.Fatalf("expected guarded batch chat request, got: %+v", chatReq.Messages)
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_TextOnlyContentArray(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/chat/completions",
				Body:   json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
			},
		},
	}

	_, err := guarded.CreateBatch(context.Background(), "mock", req)
	if err != nil {
		t.Fatal(err)
	}
	if inner.batchReq == nil || len(inner.batchReq.Requests) != 1 {
		t.Fatalf("expected delegated batch request")
	}

	var chatReq core.ChatRequest
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &chatReq); err != nil {
		t.Fatal(err)
	}
	if len(chatReq.Messages) != 2 || chatReq.Messages[0].Role != "system" {
		t.Fatalf("expected guarded batch chat request, got: %+v", chatReq.Messages)
	}
	if got := core.ExtractTextContent(chatReq.Messages[1].Content); got != "hello" {
		t.Fatalf("batch user content = %q, want hello", got)
	}
}

func TestGuardedProvider_Responses_OverrideClearsExisting(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "new instructions")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ResponsesRequest{
		Model:        "gpt-4",
		Input:        "hello",
		Instructions: "old instructions",
	}

	_, err := guarded.Responses(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.responsesReq.Instructions != "new instructions" {
		t.Errorf("expected override, got %q", inner.responsesReq.Instructions)
	}
}

func TestGuardedProvider_Responses_InjectSkipsExisting(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "injected")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ResponsesRequest{
		Model:        "gpt-4",
		Input:        "hello",
		Instructions: "existing",
	}

	_, err := guarded.Responses(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.responsesReq.Instructions != "existing" {
		t.Errorf("inject should not change existing instructions, got %q", inner.responsesReq.Instructions)
	}
}

func TestGuardedProvider_DoesNotMutateOriginalRequest(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "new")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{Role: "system", Content: "original"},
			{Role: "user", Content: "hello"},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// Original request must be untouched
	if req.Messages[0].Content != "original" {
		t.Error("original request was mutated")
	}
}

// --- Embeddings delegation tests ---

func TestGuardedProvider_Embeddings_DelegatesDirectly(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()

	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "should not affect embeddings")
	pipeline.Add(g, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.EmbeddingRequest{Model: "text-embedding-3-small", Input: "hello"}
	resp, err := guarded.Embeddings(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Object != "list" {
		t.Errorf("expected object 'list', got %q", resp.Object)
	}
	if resp.Provider != "mock" {
		t.Errorf("expected provider 'mock', got %q", resp.Provider)
	}
}

// --- Delegation tests ---

func TestGuardedProvider_ListModels_NoGuardrails(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	guarded := NewGuardedProvider(inner, pipeline)

	resp, err := guarded.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Object != "list" {
		t.Errorf("expected 'list', got %q", resp.Object)
	}
}

func TestGuardedProvider_DelegatesSupports(t *testing.T) {
	inner := &mockRoutableProvider{
		supportsFn: func(model string) bool {
			return model == "gpt-4"
		},
	}
	pipeline := NewPipeline()
	guarded := NewGuardedProvider(inner, pipeline)

	if !guarded.Supports("gpt-4") {
		t.Error("expected Supports to return true for gpt-4")
	}
	if guarded.Supports("unknown") {
		t.Error("expected Supports to return false for unknown")
	}
}

func TestGuardedProvider_DelegatesGetProviderType(t *testing.T) {
	inner := &mockRoutableProvider{
		getProviderTypeFn: func(_ string) string {
			return "openai"
		},
	}
	pipeline := NewPipeline()
	guarded := NewGuardedProvider(inner, pipeline)

	if guarded.GetProviderType("gpt-4") != "openai" {
		t.Errorf("expected 'openai', got %q", guarded.GetProviderType("gpt-4"))
	}
}

func TestGuardedProvider_GuardrailError_BlocksRequest(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	pipeline.Add(&mockGuardrail{
		name: "blocker",
		processFn: func(_ context.Context, _ []Message) ([]Message, error) {
			return nil, core.NewInvalidRequestError("guardrail violation", nil)
		},
	}, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model:    "gpt-4",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from guardrail")
	}

	// Inner provider should not have been called
	if inner.chatReq != nil {
		t.Error("inner provider should not have been called when guardrail blocks")
	}
}

func TestGuardedProvider_GuardrailError_BlocksMultimodalRequest(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	pipeline.Add(&mockGuardrail{
		name: "blocker",
		processFn: func(_ context.Context, _ []Message) ([]Message, error) {
			return nil, core.NewInvalidRequestError("guardrail violation", nil)
		},
	}, 0)

	guarded := NewGuardedProvider(inner, pipeline)

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "hello"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
				},
			},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from guardrail")
	}
	if inner.chatReq != nil {
		t.Error("inner provider should not have been called when guardrail blocks")
	}
}

// --- Adapter unit tests ---

func TestChatToMessages(t *testing.T) {
	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hello"},
		},
	}
	msgs := chatToMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "sys" {
		t.Errorf("unexpected first message: %+v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "hello" {
		t.Errorf("unexpected second message: %+v", msgs[1])
	}
}

func TestResponsesToMessages_WithInstructions(t *testing.T) {
	req := &core.ResponsesRequest{
		Model:        "gpt-4",
		Input:        "hello",
		Instructions: "be helpful",
	}
	msgs := responsesToMessages(req)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "be helpful" {
		t.Errorf("unexpected message: %+v", msgs[0])
	}
}

func TestResponsesToMessages_NoInstructions(t *testing.T) {
	req := &core.ResponsesRequest{Model: "gpt-4", Input: "hello"}
	msgs := responsesToMessages(req)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestApplyMessagesToResponses_SystemToInstructions(t *testing.T) {
	req := &core.ResponsesRequest{Model: "gpt-4", Input: "hello"}
	msgs := []Message{
		{Role: "system", Content: "new instructions"},
	}
	result := applyMessagesToResponses(req, msgs)
	if result.Instructions != "new instructions" {
		t.Errorf("expected 'new instructions', got %q", result.Instructions)
	}
	// Original untouched
	if req.Instructions != "" {
		t.Error("original request was mutated")
	}
}

func TestApplyMessagesToResponses_NoSystem_ClearsInstructions(t *testing.T) {
	req := &core.ResponsesRequest{
		Model:        "gpt-4",
		Input:        "hello",
		Instructions: "old",
	}
	msgs := []Message{} // no system messages
	result := applyMessagesToResponses(req, msgs)
	if result.Instructions != "" {
		t.Errorf("expected empty instructions, got %q", result.Instructions)
	}
}
