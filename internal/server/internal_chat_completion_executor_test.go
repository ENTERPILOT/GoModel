package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"gomodel/internal/auditlog"
	"gomodel/internal/cache"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
)

type contextCapturingProvider struct {
	capturingProvider
	capturedCtx context.Context
	chatCalls   int
}

func (p *contextCapturingProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	p.capturedCtx = ctx
	p.chatCalls++
	return p.capturingProvider.ChatCompletion(ctx, req)
}

func TestInternalChatCompletionExecutor_UsesTranslatedPlanAndAuditMetadata(t *testing.T) {
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}
	provider := &contextCapturingProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"rewrite-model"},
				providerTypes: map[string]string{
					"rewrite-model": "openai",
				},
				response: &core.ChatResponse{
					ID:       "chatcmpl-internal-1",
					Object:   "chat.completion",
					Model:    "rewrite-model",
					Provider: "openai",
					Choices: []core.Choice{
						{
							Index:        0,
							FinishReason: "stop",
							Message: core.ResponseMessage{
								Role:    "assistant",
								Content: "rewritten",
							},
						},
					},
				},
			},
		},
	}

	var capturedSelector core.WorkflowSelector
	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		WorkflowPolicyResolver: requestWorkflowPolicyResolverFunc(func(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error) {
			capturedSelector = selector
			return &core.ResolvedWorkflowPolicy{
				VersionID:      "workflow-guardrail",
				ScopeUserPath:  selector.UserPath,
				GuardrailsHash: "hash-should-be-cleared",
				Features: core.WorkflowFeatures{
					Cache:      true,
					Audit:      true,
					Usage:      true,
					Guardrails: true,
					Fallback:   true,
				},
			}, nil
		}),
		AuditLogger: logger,
	})

	ctx := core.WithRequestSnapshot(context.Background(), &core.RequestSnapshot{
		UserPath: "/team/alpha/guardrails/privacy",
	})
	resp, err := executor.ChatCompletion(ctx, &core.ChatRequest{
		Model: "rewrite-model",
		Messages: []core.Message{
			{Role: "user", Content: "John Smith"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	if resp == nil || resp.Provider != "openai" {
		t.Fatalf("resp = %#v, want openai response", resp)
	}
	if capturedSelector.UserPath != "/team/alpha/guardrails/privacy" {
		t.Fatalf("selector.UserPath = %q, want /team/alpha/guardrails/privacy", capturedSelector.UserPath)
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected provider chat request to be captured")
	}
	if len(provider.capturedChatReq.Messages) != 1 || provider.capturedChatReq.Messages[0].Role != "user" {
		t.Fatalf("provider messages = %#v, want unpatched user-only request", provider.capturedChatReq.Messages)
	}
	if origin := core.GetRequestOrigin(provider.capturedCtx); origin != core.RequestOriginGuardrail {
		t.Fatalf("provider request origin = %q, want %q", origin, core.RequestOriginGuardrail)
	}

	if len(logger.entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.Path != "/v1/chat/completions" {
		t.Fatalf("audit path = %q, want /v1/chat/completions", entry.Path)
	}
	if entry.UserPath != "/team/alpha/guardrails/privacy" {
		t.Fatalf("audit user path = %q, want /team/alpha/guardrails/privacy", entry.UserPath)
	}
	if entry.WorkflowVersionID != "workflow-guardrail" {
		t.Fatalf("audit workflow version = %q, want workflow-guardrail", entry.WorkflowVersionID)
	}
	if entry.Data == nil || entry.Data.WorkflowFeatures == nil {
		t.Fatalf("audit workflow features = %#v, want populated snapshot", entry.Data)
	}
	if entry.Data.WorkflowFeatures.Guardrails {
		t.Fatalf("audit guardrails feature = true, want false for internal guardrail calls")
	}
}

func TestInternalChatCompletionExecutor_DoesNotReuseParentWorkflowResolution(t *testing.T) {
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}
	provider := &contextCapturingProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"gpt-4o-mini"},
				providerTypes: map[string]string{
					"openai/gpt-4o-mini": "openai",
				},
				response: &core.ChatResponse{
					ID:       "chatcmpl-internal-2",
					Object:   "chat.completion",
					Model:    "gpt-4o-mini",
					Provider: "openai",
					Choices: []core.Choice{
						{
							Index:        0,
							FinishReason: "stop",
							Message: core.ResponseMessage{
								Role:    "assistant",
								Content: "rewritten",
							},
						},
					},
				},
			},
		},
	}

	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		AuditLogger: logger,
	})

	parentCtx := core.WithWorkflow(context.Background(), &core.Workflow{
		RequestID: "outer-request",
		Resolution: &core.RequestModelResolution{
			Requested:        core.NewRequestedModelSelector("gpt-5-nano", "openai"),
			ResolvedSelector: core.ModelSelector{Provider: "openai", Model: "gpt-5-nano"},
			ProviderType:     "openai",
		},
	})

	resp, err := executor.ChatCompletion(parentCtx, &core.ChatRequest{
		Model:    "gpt-4o-mini",
		Provider: "openai",
		Messages: []core.Message{
			{Role: "user", Content: "rewrite this"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp == nil || resp.Model != "gpt-4o-mini" {
		t.Fatalf("resp = %#v, want gpt-4o-mini response", resp)
	}
	if provider.capturedChatReq == nil {
		t.Fatal("expected provider chat request to be captured")
	}
	if provider.capturedChatReq.Model != "gpt-4o-mini" {
		t.Fatalf("provider request model = %q, want gpt-4o-mini", provider.capturedChatReq.Model)
	}
	if provider.capturedChatReq.Provider != "openai" {
		t.Fatalf("provider request provider = %q, want openai", provider.capturedChatReq.Provider)
	}

	if len(logger.entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.RequestedModel != "openai/gpt-4o-mini" {
		t.Fatalf("audit requested model = %q, want openai/gpt-4o-mini", entry.RequestedModel)
	}
	if entry.ResolvedModel != "openai/gpt-4o-mini" {
		t.Fatalf("audit resolved model = %q, want openai/gpt-4o-mini", entry.ResolvedModel)
	}
}

func TestInternalChatCompletionExecutor_PreservesBoundedAuditCapture(t *testing.T) {
	logger := &capturingAuditLogger{
		config: auditlog.Config{
			Enabled:    true,
			LogBodies:  true,
			LogHeaders: true,
		},
	}
	bigPrompt := strings.Repeat("x", auditlog.MaxBodyCapture+2048)
	bigResponse := strings.Repeat("y", auditlog.MaxBodyCapture+2048)
	provider := &contextCapturingProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"rewrite-model"},
				providerTypes: map[string]string{
					"rewrite-model": "openai",
				},
				response: &core.ChatResponse{
					ID:       "chatcmpl-internal-3",
					Object:   "chat.completion",
					Model:    "rewrite-model",
					Provider: "openai",
					Choices: []core.Choice{
						{
							Index:        0,
							FinishReason: "stop",
							Message: core.ResponseMessage{
								Role:    "assistant",
								Content: bigResponse,
							},
						},
					},
				},
			},
		},
	}

	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		AuditLogger: logger,
	})

	ctx := context.Background()
	ctx = core.WithRequestID(ctx, "req-guardrail-1")
	ctx = core.WithRequestSnapshot(ctx, core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{"Traceparent": []string{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}},
		"application/json",
		nil,
		false,
		"req-guardrail-1",
		map[string]string{"Traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		"/team/alpha/guardrails/privacy",
	))

	_, err := executor.ChatCompletion(ctx, &core.ChatRequest{
		Model: "rewrite-model",
		Messages: []core.Message{
			{Role: "user", Content: bigPrompt},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(logger.entries))
	}

	entry := logger.entries[0]
	if entry.Data == nil {
		t.Fatal("audit data = nil, want populated capture data")
	}
	if entry.Data.RequestHeaders["Content-Type"] != "application/json" {
		t.Fatalf("request Content-Type = %q, want application/json", entry.Data.RequestHeaders["Content-Type"])
	}
	if entry.Data.RequestHeaders["Traceparent"] == "" {
		t.Fatal("request Traceparent header missing from audit capture")
	}
	if entry.Data.ResponseHeaders["Content-Type"] != "application/json" {
		t.Fatalf("response Content-Type = %q, want application/json", entry.Data.ResponseHeaders["Content-Type"])
	}
	if !entry.Data.RequestBodyTooBigToHandle {
		t.Fatal("RequestBodyTooBigToHandle = false, want true")
	}
	if entry.Data.RequestBody != nil {
		t.Fatalf("request body = %#v, want omitted body for oversized payload", entry.Data.RequestBody)
	}
	if !entry.Data.ResponseBodyTooBigToHandle {
		t.Fatal("ResponseBodyTooBigToHandle = false, want true")
	}
	if entry.Data.ResponseBody == nil {
		t.Fatal("response body = nil, want truncated captured payload")
	}
}

func TestInternalChatCompletionExecutor_RoutesThroughResponseCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &contextCapturingProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"rewrite-model"},
				providerTypes: map[string]string{
					"rewrite-model": "openai",
				},
				response: &core.ChatResponse{
					ID:       "chatcmpl-internal-cache",
					Object:   "chat.completion",
					Model:    "rewrite-model",
					Provider: "openai",
					Choices: []core.Choice{
						{
							Index:        0,
							FinishReason: "stop",
							Message: core.ResponseMessage{
								Role:    "assistant",
								Content: "rewritten",
							},
						},
					},
				},
			},
		},
	}

	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		ResponseCache: rcm,
	})

	req := &core.ChatRequest{
		Model: "rewrite-model",
		Messages: []core.Message{
			{Role: "user", Content: "John Smith"},
		},
	}

	resp1, err := executor.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("first ChatCompletion() error = %v", err)
	}
	if err := rcm.Close(); err != nil {
		t.Fatalf("ResponseCacheMiddleware.Close() error = %v", err)
	}

	resp2, err := executor.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("second ChatCompletion() error = %v", err)
	}

	if provider.chatCalls != 1 {
		t.Fatalf("provider chat calls = %d, want 1 with second response served from cache", provider.chatCalls)
	}
	if resp1 == nil || resp2 == nil || resp2.Choices[0].Message.Content != resp1.Choices[0].Message.Content {
		t.Fatalf("responses = %#v / %#v, want identical cached response", resp1, resp2)
	}
}

func TestInternalChatCompletionExecutor_CachedNilWorkflowDoesNotPanic(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &contextCapturingProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"rewrite-model"},
				providerTypes: map[string]string{
					"rewrite-model": "openai",
				},
				response: &core.ChatResponse{
					ID:       "chatcmpl-internal-cache-nil-workflow",
					Object:   "chat.completion",
					Model:    "rewrite-model",
					Provider: "openai",
					Choices: []core.Choice{
						{
							Index:        0,
							FinishReason: "stop",
							Message: core.ResponseMessage{
								Role:    "assistant",
								Content: "rewritten",
							},
						},
					},
				},
			},
		},
	}

	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		ResponseCache: rcm,
	})
	req := &core.ChatRequest{
		Model: "rewrite-model",
		Messages: []core.Message{
			{Role: "user", Content: "John Smith"},
		},
	}

	_, _, _, _, _, cacheType, err := executor.executeChatCompletion(context.Background(), nil, req)
	if err != nil {
		t.Fatalf("first executeChatCompletion() error = %v", err)
	}
	if cacheType != "" {
		t.Fatalf("first cacheType = %q, want empty", cacheType)
	}
	if err := rcm.Close(); err != nil {
		t.Fatalf("ResponseCacheMiddleware.Close() error = %v", err)
	}

	resp, providerType, providerName, _, _, cacheType, err := executor.executeChatCompletion(context.Background(), nil, req)
	if err != nil {
		t.Fatalf("cached executeChatCompletion() error = %v", err)
	}
	if provider.chatCalls != 1 {
		t.Fatalf("provider chat calls = %d, want 1 with second response served from cache", provider.chatCalls)
	}
	if resp == nil || resp.ID != "chatcmpl-internal-cache-nil-workflow" {
		t.Fatalf("resp = %#v, want cached provider response", resp)
	}
	if providerType != "" {
		t.Fatalf("providerType = %q, want empty for nil workflow cache hit", providerType)
	}
	if providerName != "" {
		t.Fatalf("providerName = %q, want empty for nil workflow cache hit", providerName)
	}
	if cacheType != responsecache.CacheTypeExact {
		t.Fatalf("cacheType = %q, want %q", cacheType, responsecache.CacheTypeExact)
	}
}

func TestInternalChatCompletionExecutor_MarshalFailureFallsBackToNoCacheDispatch(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &contextCapturingProvider{
		capturingProvider: capturingProvider{
			mockProvider: mockProvider{
				supportedModels: []string{"rewrite-model"},
				providerTypes: map[string]string{
					"rewrite-model": "openai",
				},
				response: &core.ChatResponse{
					ID:       "chatcmpl-internal-marshal-fallback",
					Object:   "chat.completion",
					Model:    "rewrite-model",
					Provider: "openai",
					Choices: []core.Choice{
						{
							Index:        0,
							FinishReason: "stop",
							Message: core.ResponseMessage{
								Role:    "assistant",
								Content: "rewritten",
							},
						},
					},
				},
			},
		},
	}

	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		ResponseCache: rcm,
	})

	resp, err := executor.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "rewrite-model",
		Messages: []core.Message{
			{Role: "user", Content: "John Smith"},
		},
		Tools: []map[string]any{{"unsupported": func() {}}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp == nil || resp.ID != "chatcmpl-internal-marshal-fallback" {
		t.Fatalf("resp = %#v, want provider response", resp)
	}
	if provider.chatCalls != 1 {
		t.Fatalf("provider chat calls = %d, want one no-cache dispatch", provider.chatCalls)
	}
}
