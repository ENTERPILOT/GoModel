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
	createBatchErr    error
	fileContent       *core.FileContentResponse
	fileCreates       []*core.FileCreateRequest
	fileDeletes       []string
	fileObject        *core.FileObject
	passthroughReq    *core.PassthroughRequest
	passthroughType   string
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
	if m.createBatchErr != nil {
		return nil, m.createBatchErr
	}
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

func (m *mockRoutableProvider) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	resp, err := m.CreateBatch(ctx, providerType, req)
	return resp, map[string]string{"chat-1": "/v1/chat/completions"}, err
}

func (m *mockRoutableProvider) GetBatchResultsWithHints(_ context.Context, _, _ string, _ map[string]string) (*core.BatchResultsResponse, error) {
	return &core.BatchResultsResponse{Object: "list", BatchID: "batch_1"}, nil
}

func (m *mockRoutableProvider) ClearBatchResultHints(_ string, _ string) {}

func (m *mockRoutableProvider) CreateFile(_ context.Context, _ string, req *core.FileCreateRequest) (*core.FileObject, error) {
	copy := *req
	copy.Content = append([]byte(nil), req.Content...)
	m.fileCreates = append(m.fileCreates, &copy)
	if m.fileObject != nil {
		return m.fileObject, nil
	}
	return &core.FileObject{ID: "file_rewritten", Object: "file", Filename: req.Filename, Purpose: req.Purpose}, nil
}

func (m *mockRoutableProvider) ListFiles(_ context.Context, _ string, _ string, _ int, _ string) (*core.FileListResponse, error) {
	return &core.FileListResponse{Object: "list"}, nil
}

func (m *mockRoutableProvider) GetFile(_ context.Context, _ string, id string) (*core.FileObject, error) {
	return &core.FileObject{ID: id, Object: "file"}, nil
}

func (m *mockRoutableProvider) DeleteFile(_ context.Context, _ string, id string) (*core.FileDeleteResponse, error) {
	m.fileDeletes = append(m.fileDeletes, id)
	return &core.FileDeleteResponse{ID: id, Object: "file", Deleted: true}, nil
}

func (m *mockRoutableProvider) GetFileContent(_ context.Context, _ string, id string) (*core.FileContentResponse, error) {
	if m.fileContent != nil {
		return m.fileContent, nil
	}
	return &core.FileContentResponse{ID: id, Filename: "batch.jsonl", Data: []byte("{}\n")}, nil
}

func (m *mockRoutableProvider) Passthrough(_ context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	m.passthroughType = providerType
	m.passthroughReq = req
	return &core.PassthroughResponse{
		StatusCode: http.StatusAccepted,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
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
	parallelToolCalls := false
	req := &core.ChatRequest{
		Model:             "gpt-4",
		Temperature:       &temp,
		MaxTokens:         &maxTok,
		Tools:             []map[string]any{{"type": "function"}},
		ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
		ParallelToolCalls: &parallelToolCalls,
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup_weather",
							Arguments: `{"city":"Warsaw"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_123", Content: `{"temperature_c":21}`},
		},
		Stream:    true,
		Reasoning: &core.Reasoning{Effort: "high"},
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
	if len(got.Tools) != 1 {
		t.Errorf("tools not preserved")
	}
	if got.ToolChoice == nil {
		t.Errorf("tool_choice not preserved")
	}
	if got.ParallelToolCalls == nil || *got.ParallelToolCalls {
		t.Errorf("parallel_tool_calls not preserved")
	}
	if !got.Stream {
		t.Errorf("stream not preserved")
	}
	if got.Reasoning == nil || got.Reasoning.Effort != "high" {
		t.Errorf("reasoning not preserved")
	}
	if len(got.Messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3", len(got.Messages))
	}
	if len(got.Messages[1].ToolCalls) != 1 || got.Messages[1].ToolCalls[0].ID != "call_123" {
		t.Errorf("assistant tool_calls not preserved: %+v", got.Messages[1].ToolCalls)
	}
	if got.Messages[2].ToolCallID != "call_123" {
		t.Errorf("tool_call_id not preserved: %+v", got.Messages[2])
	}
}

func TestChatAdaptersCloneToolCalls(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup_weather",
							Arguments: `{"city":"Warsaw"}`,
						},
					},
				},
			},
		},
	}

	msgs, err := chatToMessages(req)
	if err != nil {
		t.Fatalf("chatToMessages() error = %v", err)
	}
	req.Messages[0].ToolCalls[0].Function.Name = "mutated"
	if msgs[0].ToolCalls[0].Function.Name != "lookup_weather" {
		t.Fatalf("chatToMessages should clone tool calls, got %+v", msgs[0].ToolCalls)
	}

	chatMsg := newChatMessageFromGuardrail(msgs[0])
	msgs[0].ToolCalls[0].Function.Name = "mutated-again"
	if chatMsg.ToolCalls[0].Function.Name != "lookup_weather" {
		t.Fatalf("newChatMessageFromGuardrail should clone tool calls, got %+v", chatMsg.ToolCalls)
	}
}

func TestChatAdaptersPreserveContentNull(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role:        "assistant",
				ContentNull: true,
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup_weather",
							Arguments: `{"city":"Warsaw"}`,
						},
					},
				},
			},
		},
	}

	msgs, err := chatToMessages(req)
	if err != nil {
		t.Fatalf("chatToMessages() error = %v", err)
	}
	if !msgs[0].ContentNull {
		t.Fatal("chatToMessages should preserve ContentNull")
	}

	chatMsg := newChatMessageFromGuardrail(msgs[0])
	if !chatMsg.ContentNull {
		t.Fatal("newChatMessageFromGuardrail should preserve ContentNull")
	}
}

func TestNewChatMessageFromGuardrail_ClearsContentNullWhenContentPresent(t *testing.T) {
	msgs := []Message{
		{
			Role:        "assistant",
			Content:     "I'll check that now.",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:   "call_123",
					Type: "function",
					Function: core.FunctionCall{
						Name:      "lookup_weather",
						Arguments: `{"city":"Warsaw"}`,
					},
				},
			},
		},
	}

	chatMsg := newChatMessageFromGuardrail(msgs[0])
	if chatMsg.Content != "I'll check that now." {
		t.Fatalf("Content = %q, want assistant text", chatMsg.Content)
	}
	if chatMsg.ContentNull {
		t.Fatal("newChatMessageFromGuardrail should clear ContentNull when Content is present")
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
	parts, ok := inner.chatReq.Messages[1].Content.([]core.ContentPart)
	if !ok || len(parts) != 1 || parts[0].Text != "hello" {
		t.Fatalf("expected text-only content array to be preserved, got %#v", inner.chatReq.Messages[1].Content)
	}

	originalParts, ok := req.Messages[0].Content.([]core.ContentPart)
	if !ok || len(originalParts) != 1 || originalParts[0].Text != "hello" {
		t.Fatalf("original request content mutated: %#v", req.Messages[0].Content)
	}
}

func TestGuardedProvider_ChatCompletion_RewritesStructuredTextContentWithoutDroppingOpaqueFields(t *testing.T) {
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

	var req core.ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-4",
		"messages":[
			{
				"role":"user",
				"name":"alice",
				"x_meta":{"tier":"gold"},
				"content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]
			}
		]
	}`), &req); err != nil {
		t.Fatal(err)
	}

	_, err := guarded.ChatCompletion(context.Background(), &req)
	if err != nil {
		t.Fatal(err)
	}

	if inner.chatReq == nil || len(inner.chatReq.Messages) != 1 {
		t.Fatalf("expected rewritten request, got %+v", inner.chatReq)
	}
	if inner.chatReq.Messages[0].ExtraFields["name"] == nil {
		t.Fatalf("message name missing from ExtraFields: %+v", inner.chatReq.Messages[0].ExtraFields)
	}
	if inner.chatReq.Messages[0].ExtraFields["x_meta"] == nil {
		t.Fatalf("message x_meta missing from ExtraFields: %+v", inner.chatReq.Messages[0].ExtraFields)
	}
	parts, ok := inner.chatReq.Messages[0].Content.([]core.ContentPart)
	if !ok || len(parts) != 1 {
		t.Fatalf("expected structured content to be preserved, got %#v", inner.chatReq.Messages[0].Content)
	}
	if parts[0].Text != "hello [rewritten]" {
		t.Fatalf("parts[0].Text = %q, want rewritten text", parts[0].Text)
	}
	if parts[0].ExtraFields["cache_control"] == nil {
		t.Fatalf("cache_control missing from content part extra fields: %+v", parts[0].ExtraFields)
	}

	originalParts, ok := req.Messages[0].Content.([]core.ContentPart)
	if !ok || len(originalParts) != 1 || originalParts[0].Text != "hello" {
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
	if parts[0].Type != "text" || parts[0].Text != "describe [rewritten]" {
		t.Fatalf("expected rewritten text merged into multimodal content, got %#v", parts[0])
	}
	if got := core.ExtractTextContent(inner.chatReq.Messages[1].Content); got != "plain text [rewritten]" {
		t.Fatalf("expected rewritten text-only message, got %q", got)
	}
}

func TestGuardedProvider_ChatCompletion_RewritesMultimodalMessageWithMultipleTextParts(t *testing.T) {
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
					{Type: "text", Text: "before"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
					{Type: "text", Text: "after"},
				},
			},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want multimodal rewrite to succeed", err)
	}

	if inner.chatReq == nil {
		t.Fatal("inner provider was not called")
	}

	parts, ok := inner.chatReq.Messages[0].Content.([]core.ContentPart)
	if !ok {
		t.Fatalf("Messages[0].Content type = %T, want []core.ContentPart", inner.chatReq.Messages[0].Content)
	}

	if got := core.ExtractTextContent(parts); got != "before after [rewritten]" {
		t.Fatalf("ExtractTextContent(Messages[0].Content) = %q, want %q", got, "before after [rewritten]")
	}

	imageParts := 0
	for _, part := range parts {
		if part.Type == "image_url" {
			imageParts++
		}
	}
	if imageParts != 1 {
		t.Fatalf("expected one preserved image part, got %+v", parts)
	}
}

func TestGuardedProvider_ChatCompletion_PreservesToolCallsWithoutMultimodalContent(t *testing.T) {
	inner := &mockRoutableProvider{}
	guarded := NewGuardedProvider(inner, NewPipeline())

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{
				Role:    "assistant",
				Content: nil,
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup",
							Arguments: "{}",
						},
					},
				},
			},
			{Role: "user", Content: "continue"},
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
		t.Fatalf("len(Messages) = %d, want 2", len(inner.chatReq.Messages))
	}
	if len(inner.chatReq.Messages[0].ToolCalls) != 1 || inner.chatReq.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected tool_calls to be preserved, got %+v", inner.chatReq.Messages[0].ToolCalls)
	}
}

func TestGuardedProvider_ChatCompletion_RejectsUnsupportedContent(t *testing.T) {
	inner := &mockRoutableProvider{}
	guarded := NewGuardedProvider(inner, NewPipeline())

	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{Role: "user", Content: 123},
		},
	}

	_, err := guarded.ChatCompletion(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if inner.chatReq != nil {
		t.Fatal("inner provider should not have been called")
	}
}

func TestApplyMessagesToChatPreservingEnvelope_PreservesOriginalEnvelope(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role: "assistant",
				Content: []core.ContentPart{
					{Type: "text", Text: "describe"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
				},
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup",
							Arguments: "{}",
						},
					},
				},
			},
		},
	}

	result, err := applyMessagesToChatPreservingEnvelope(req, []Message{
		{
			Role:      "assistant",
			Content:   "describe [rewritten]",
			ToolCalls: cloneToolCalls(req.Messages[0].ToolCalls),
		},
	})
	if err != nil {
		t.Fatalf("applyMessagesToChatPreservingEnvelope() error = %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}
	if len(result.Messages[0].ToolCalls) != 1 || result.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected tool_calls to be preserved, got %+v", result.Messages[0].ToolCalls)
	}
	parts, ok := result.Messages[0].Content.([]core.ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("unexpected merged content: %#v", result.Messages[0].Content)
	}
	if parts[0].Text != "describe [rewritten]" || parts[1].Type != "image_url" {
		t.Fatalf("unexpected merged parts: %+v", parts)
	}
}

func TestApplyMessagesToChatPreservingEnvelope_RejectsDroppedMessages(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "keep"},
					{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
				},
			},
			{Role: "assistant", Content: "drop me"},
		},
	}

	_, err := applyMessagesToChatPreservingEnvelope(req, []Message{
		{Role: "user", Content: "keep [rewritten]"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestApplyMessagesToChatPreservingEnvelope_ClearsToolMetadataWhenGuardedMessageRemovesIt(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role: "assistant",
				Content: []core.ContentPart{
					{Type: "text", Text: "before"},
				},
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup",
							Arguments: "{}",
						},
					},
				},
				ToolCallID: "call_1",
			},
		},
	}

	result, err := applyMessagesToChatPreservingEnvelope(req, []Message{
		{Role: "assistant", Content: "after"},
	})
	if err != nil {
		t.Fatalf("applyMessagesToChatPreservingEnvelope() error = %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}
	if len(result.Messages[0].ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want empty", result.Messages[0].ToolCalls)
	}
	if result.Messages[0].ToolCallID != "" {
		t.Fatalf("ToolCallID = %q, want empty", result.Messages[0].ToolCallID)
	}
}

func TestApplyMessagesToChatPreservingEnvelope_TailMatchesExistingSystemMessages(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role:        "system",
				Content:     "original system",
				ExtraFields: map[string]json.RawMessage{"x_system": json.RawMessage(`true`)},
			},
			{Role: "user", Content: "hello"},
		},
	}

	result, err := applyMessagesToChatPreservingEnvelope(req, []Message{
		{Role: "system", Content: "prepended system"},
		{Role: "system", Content: "rewritten original system"},
		{Role: "user", Content: "hello"},
	})
	if err != nil {
		t.Fatalf("applyMessagesToChatPreservingEnvelope() error = %v", err)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(result.Messages))
	}
	if result.Messages[0].ExtraFields["x_system"] != nil {
		t.Fatalf("prepended system message should not inherit original extras: %+v", result.Messages[0].ExtraFields)
	}
	if result.Messages[1].ExtraFields["x_system"] == nil {
		t.Fatalf("tail-matched system message lost original extras: %+v", result.Messages[1].ExtraFields)
	}
}

func TestPatchChatMessagesJSON_TailMatchesExistingSystemMessages(t *testing.T) {
	originalRaw := json.RawMessage(`[
		{"role":"system","content":"original system","x_system":true},
		{"role":"user","content":"hello"}
	]`)
	original := []core.Message{
		{
			Role:        "system",
			Content:     "original system",
			ExtraFields: map[string]json.RawMessage{"x_system": json.RawMessage(`true`)},
		},
		{Role: "user", Content: "hello"},
	}
	modified := []core.Message{
		{Role: "system", Content: "prepended system"},
		{
			Role:        "system",
			Content:     "rewritten original system",
			ExtraFields: map[string]json.RawMessage{"x_system": json.RawMessage(`true`)},
		},
		{Role: "user", Content: "hello"},
	}

	patched, err := patchChatMessagesJSON(originalRaw, original, modified)
	if err != nil {
		t.Fatalf("patchChatMessagesJSON() error = %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(patched, &decoded); err != nil {
		t.Fatalf("failed to unmarshal patched payload: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("len(decoded) = %d, want 3", len(decoded))
	}
	if decoded[0]["x_system"] != nil {
		t.Fatalf("prepended system message should not inherit original extras: %#v", decoded[0])
	}
	if decoded[1]["x_system"] != true {
		t.Fatalf("tail-matched system message lost original extras: %#v", decoded[1])
	}
}

func TestApplyMessagesToChatPreservingEnvelope_RejectsShiftedNonSystemTurns(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup",
							Arguments: "{}",
						},
					},
				},
				ContentNull: true,
			},
			{Role: "tool", ToolCallID: "call_1", Content: "{}"},
		},
	}

	_, err := applyMessagesToChatPreservingEnvelope(req, []Message{
		{Role: "tool", Content: "{}"},
		{Role: "assistant", Content: ""},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRewriteStructuredContentWithTextRewrite_MergesMultipleTextParts(t *testing.T) {
	merged, err := rewriteStructuredContentWithTextRewrite([]core.ContentPart{
		{Type: "text", Text: "before"},
		{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
		{Type: "text", Text: "after"},
	}, "rewritten")
	if err != nil {
		t.Fatalf("rewriteStructuredContentWithTextRewrite() error = %v", err)
	}
	if got := core.ExtractTextContent(merged); got != "rewritten" {
		t.Fatalf("ExtractTextContent(merged) = %q, want rewritten", got)
	}
	parts, ok := merged.([]core.ContentPart)
	if !ok {
		t.Fatalf("merged type = %T, want []core.ContentPart", merged)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Fatalf("unexpected merged content: %+v", parts)
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

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_InputFile(t *testing.T) {
	inner := &mockRoutableProvider{
		fileContent: &core.FileContentResponse{
			ID:       "file_source",
			Filename: "batch.jsonl",
			Data:     []byte("{\"custom_id\":\"chat-1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}}\n"),
		},
		fileObject: &core.FileObject{ID: "file_rewritten", Object: "file", Filename: "batch.jsonl", Purpose: "batch"},
	}
	pipeline := NewPipeline()
	gr, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(gr, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	_, err := guarded.CreateBatch(context.Background(), "mock", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inner.batchReq == nil {
		t.Fatal("expected delegated batch request")
	}
	if inner.batchReq.InputFileID != "file_rewritten" {
		t.Fatalf("input_file_id = %q, want file_rewritten", inner.batchReq.InputFileID)
	}
	if len(inner.fileCreates) != 1 {
		t.Fatalf("len(fileCreates) = %d, want 1", len(inner.fileCreates))
	}
	if got := string(inner.fileCreates[0].Content); !strings.Contains(got, "\"role\":\"system\"") {
		t.Fatalf("rewritten file content = %s, want injected system message", got)
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_InputFileCleansUpOnFailure(t *testing.T) {
	inner := &mockRoutableProvider{
		createBatchErr: context.Canceled,
		fileContent: &core.FileContentResponse{
			ID:       "file_source",
			Filename: "batch.jsonl",
			Data:     []byte("{\"custom_id\":\"chat-1\",\"method\":\"POST\",\"url\":\"/v1/chat/completions\",\"body\":{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}}\n"),
		},
		fileObject: &core.FileObject{ID: "file_rewritten", Object: "file", Filename: "batch.jsonl", Purpose: "batch"},
	}
	pipeline := NewPipeline()
	gr, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(gr, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	_, err := guarded.CreateBatch(context.Background(), "mock", &core.BatchRequest{
		InputFileID: "file_source",
		Endpoint:    "/v1/chat/completions",
	})
	if err == nil {
		t.Fatal("CreateBatch() error = nil, want non-nil")
	}
	if len(inner.fileDeletes) != 1 || inner.fileDeletes[0] != "file_rewritten" {
		t.Fatalf("fileDeletes = %v, want [file_rewritten]", inner.fileDeletes)
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_SkipsEmbeddingsItems(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptInject, "guardrail system")
	pipeline.Add(g, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/embeddings",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/embeddings",
				Body:   json.RawMessage(`{"model":"text-embedding-3-small","input":"hello"}`),
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
	if string(inner.batchReq.Requests[0].Body) != `{"model":"text-embedding-3-small","input":"hello"}` {
		t.Fatalf("expected embeddings batch item to remain unchanged, got %s", inner.batchReq.Requests[0].Body)
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
	parts, ok := chatReq.Messages[1].Content.([]core.ContentPart)
	if !ok || len(parts) != 1 || parts[0].Text != "hello" {
		t.Fatalf("expected batch structured content to be preserved, got %#v", chatReq.Messages[1].Content)
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_RewritesStructuredTextContentWithoutDroppingOpaqueFields(t *testing.T) {
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
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/chat/completions",
				Body: json.RawMessage(`{
					"model":"gpt-4",
					"messages":[
						{
							"role":"user",
							"name":"alice",
							"x_meta":{"tier":"gold"},
							"content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]
						}
					]
				}`),
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

	var body map[string]any
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want 1 entry", body["messages"])
	}
	userMsg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %#v, want object", messages[0])
	}
	if userMsg["name"] != "alice" {
		t.Fatalf("messages[0].name = %#v, want alice", userMsg["name"])
	}
	xMeta, ok := userMsg["x_meta"].(map[string]any)
	if !ok || xMeta["tier"] != "gold" {
		t.Fatalf("messages[0].x_meta = %#v, want preserved nested metadata", userMsg["x_meta"])
	}
	content, ok := userMsg["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("messages[0].content = %#v, want preserved content array", userMsg["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0].content[0] = %#v, want object", content[0])
	}
	if part["text"] != "hello [rewritten]" {
		t.Fatalf("messages[0].content[0].text = %#v, want rewritten text", part["text"])
	}
	if part["cache_control"] == nil {
		t.Fatalf("messages[0].content[0].cache_control = %#v, want preserved metadata", part["cache_control"])
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_PreservesOpaqueChatFields(t *testing.T) {
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
				Body: json.RawMessage(`{
					"model":"gpt-4",
					"messages":[{"role":"user","content":"hello","name":"alice","x_meta":{"tier":"gold"}}],
					"response_format":{"type":"json_schema","json_schema":{"name":"reply"}}
				}`),
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

	var body map[string]any
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}

	responseFormat, ok := body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format = %#v, want object", body["response_format"])
	}
	if responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format.type = %#v, want json_schema", responseFormat["type"])
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2 entries", body["messages"])
	}
	systemMsg, ok := messages[0].(map[string]any)
	if !ok || systemMsg["role"] != "system" {
		t.Fatalf("messages[0] = %#v, want injected system message", messages[0])
	}
	userMsg, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("messages[1] = %#v, want object", messages[1])
	}
	if userMsg["name"] != "alice" {
		t.Fatalf("messages[1].name = %#v, want alice", userMsg["name"])
	}
	xMeta, ok := userMsg["x_meta"].(map[string]any)
	if !ok || xMeta["tier"] != "gold" {
		t.Fatalf("messages[1].x_meta = %#v, want preserved nested metadata", userMsg["x_meta"])
	}
	if userMsg["content"] != "hello" {
		t.Fatalf("messages[1].content = %#v, want hello", userMsg["content"])
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_RewritesChatContentWithoutDroppingOpaqueMessageFields(t *testing.T) {
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
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/chat/completions",
				Body: json.RawMessage(`{
					"model":"gpt-4",
					"messages":[{"role":"user","content":"hello","name":"alice","x_meta":{"tier":"gold"}}]
				}`),
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

	var body map[string]any
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v, want 1 entry", body["messages"])
	}
	userMsg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %#v, want object", messages[0])
	}
	if userMsg["content"] != "hello [rewritten]" {
		t.Fatalf("messages[0].content = %#v, want rewritten text", userMsg["content"])
	}
	if userMsg["name"] != "alice" {
		t.Fatalf("messages[0].name = %#v, want alice", userMsg["name"])
	}
	xMeta, ok := userMsg["x_meta"].(map[string]any)
	if !ok || xMeta["tier"] != "gold" {
		t.Fatalf("messages[0].x_meta = %#v, want preserved nested metadata", userMsg["x_meta"])
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_PreservesOpaqueResponsesFields(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "guardrail instructions")
	pipeline.Add(g, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/responses",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/responses",
				Body: json.RawMessage(`{
					"model":"gpt-4",
					"instructions":"original",
					"input":[{"type":"message","role":"user","content":"hello","x_trace":{"id":"trace-1"}}],
					"response_format":{"type":"json_schema","json_schema":{"name":"reply"}}
				}`),
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

	var body map[string]any
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}

	if body["instructions"] != "guardrail instructions" {
		t.Fatalf("instructions = %#v, want guarded text", body["instructions"])
	}
	responseFormat, ok := body["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %#v, want preserved json_schema object", body["response_format"])
	}

	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %#v, want 1 entry", body["input"])
	}
	inputMsg, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] = %#v, want object", input[0])
	}
	xTrace, ok := inputMsg["x_trace"].(map[string]any)
	if !ok || xTrace["id"] != "trace-1" {
		t.Fatalf("input[0].x_trace = %#v, want preserved nested metadata", inputMsg["x_trace"])
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_NormalizesFullURLResponsesEndpoint(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptOverride, "guardrail instructions")
	pipeline.Add(g, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "https://provider.example/v1/responses/?trace=1",
				Body: json.RawMessage(`{
					"model":"gpt-4",
					"instructions":"original",
					"input":"hello"
				}`),
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

	var body map[string]any
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["instructions"] != "guardrail instructions" {
		t.Fatalf("instructions = %#v, want guarded text", body["instructions"])
	}
}

func TestGuardedProvider_CreateBatch_BatchGuardrailsEnabled_PreservesSystemMessageOpaqueFields(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	g, _ := NewSystemPromptGuardrail("test", SystemPromptDecorator, "prefix")
	pipeline.Add(g, 0)
	guarded := NewGuardedProviderWithOptions(inner, pipeline, Options{EnableForBatchProcessing: true})

	req := &core.BatchRequest{
		Endpoint: "/v1/chat/completions",
		Requests: []core.BatchRequestItem{
			{
				Method: http.MethodPost,
				URL:    "/v1/chat/completions",
				Body: json.RawMessage(`{
					"model":"gpt-4",
					"messages":[
						{"role":"system","content":"original","x_meta":{"tier":"gold"}},
						{"role":"user","content":"hello"}
					]
				}`),
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

	var body map[string]any
	if err := json.Unmarshal(inner.batchReq.Requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2 entries", body["messages"])
	}
	systemMsg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %#v, want object", messages[0])
	}
	if systemMsg["content"] != "prefix\noriginal" {
		t.Fatalf("messages[0].content = %#v, want decorated content", systemMsg["content"])
	}
	xMeta, ok := systemMsg["x_meta"].(map[string]any)
	if !ok || xMeta["tier"] != "gold" {
		t.Fatalf("messages[0].x_meta = %#v, want preserved metadata", systemMsg["x_meta"])
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

func TestGuardedProvider_ModelCount_UnknownWhenInnerDoesNotExposeCount(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	guarded := NewGuardedProvider(inner, pipeline)

	if got := guarded.ModelCount(); got != -1 {
		t.Fatalf("ModelCount() = %d, want -1 for unknown count", got)
	}
}

func TestGuardedProvider_Passthrough_Delegates(t *testing.T) {
	inner := &mockRoutableProvider{}
	pipeline := NewPipeline()
	guarded := NewGuardedProvider(inner, pipeline)

	resp, err := guarded.Passthrough(context.Background(), "openai", &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "responses",
		Body:     io.NopCloser(strings.NewReader(`{"foo":"bar"}`)),
		Headers: http.Header{
			"Content-Type": {"application/json"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if inner.passthroughType != "openai" {
		t.Fatalf("providerType = %q, want openai", inner.passthroughType)
	}
	if inner.passthroughReq == nil {
		t.Fatal("passthroughReq = nil")
	}
	if inner.passthroughReq.Endpoint != "responses" {
		t.Fatalf("Endpoint = %q, want responses", inner.passthroughReq.Endpoint)
	}
	body, readErr := io.ReadAll(inner.passthroughReq.Body)
	if readErr != nil {
		t.Fatalf("failed to read passthrough body: %v", readErr)
	}
	if got := string(body); got != `{"foo":"bar"}` {
		t.Fatalf("Body = %q, want request body", got)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusAccepted)
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
	msgs, err := chatToMessages(req)
	if err != nil {
		t.Fatalf("chatToMessages() error = %v", err)
	}
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

func TestChatToMessages_RejectsUnsupportedContent(t *testing.T) {
	req := &core.ChatRequest{
		Model: "gpt-4",
		Messages: []core.Message{
			{Role: "user", Content: map[string]any{"bad": "content"}},
		},
	}

	_, err := chatToMessages(req)
	if err == nil {
		t.Fatal("expected error, got nil")
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

func TestRewriteStructuredContentWithTextRewrite_RejectsExcessiveContentParts(t *testing.T) {
	parts := make([]core.ContentPart, 1_000_000)
	for i := range parts {
		parts[i] = core.ContentPart{
			Type:     "image_url",
			ImageURL: &core.ImageURLContent{URL: "https://example.com/img.png"},
		}
	}
	_, err := rewriteStructuredContentWithTextRewrite(parts, "rewritten")
	if err == nil {
		t.Fatal("expected error for excessive content parts, got nil")
	}
	if !strings.Contains(err.Error(), "too many content parts") {
		t.Fatalf("expected 'too many content parts' error, got: %v", err)
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
