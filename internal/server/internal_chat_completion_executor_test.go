package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsecache"
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
					Failover:   true,
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
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "openai", resp.Provider)
	require.Equal(t, "/team/alpha/guardrails/privacy", capturedSelector.UserPath)
	require.NotNil(t, provider.capturedChatReq)
	require.Len(t, provider.capturedChatReq.Messages, 1)
	require.Equal(t, "user", provider.capturedChatReq.Messages[0].Role)
	origin := core.GetRequestOrigin(provider.capturedCtx)
	require.Equal(t, core.RequestOriginGuardrail, origin)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, "/v1/chat/completions", entry.Path)
	require.Equal(t, "/team/alpha/guardrails/privacy", entry.UserPath)
	require.Equal(t, "workflow-guardrail", entry.WorkflowVersionID)
	require.NotNil(t, entry.Data)
	require.NotNil(t, entry.Data.WorkflowFeatures)
	require.False(t, entry.Data.WorkflowFeatures.Guardrails)
}

func TestInternalChatCompletionExecutor_DoesNotReuseParentWorkflowResolution(t *testing.T) {
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}
	provider := &contextCapturingProvider{
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
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "gpt-4o-mini", resp.Model)
	require.NotNil(t, provider.capturedChatReq)
	require.Equal(t, "gpt-4o-mini", provider.capturedChatReq.Model)
	require.Equal(t, "openai", provider.capturedChatReq.Provider)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, "openai/gpt-4o-mini", entry.RequestedModel)
	require.Equal(t, "openai/gpt-4o-mini", entry.ResolvedModel)
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
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.NotNil(t, entry.Data)
	require.Equal(t, "application/json", entry.Data.RequestHeaders["Content-Type"])
	require.NotEmpty(t, entry.Data.RequestHeaders["Traceparent"])
	require.Equal(t, "application/json", entry.Data.ResponseHeaders["Content-Type"])
	require.True(t, entry.Data.RequestBodyTooBigToHandle)
	require.Nil(t, entry.Data.RequestBody)
	require.True(t, entry.Data.ResponseBodyTooBigToHandle)
	require.NotNil(t, entry.Data.ResponseBody)
}

func TestInternalChatCompletionExecutor_RoutesThroughResponseCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &contextCapturingProvider{
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
	require.NoError(t, err)
	err = rcm.Close()
	require.NoError(t, err)

	resp2, err := executor.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, provider.chatCalls)
	require.NotNil(t, resp1)
	require.NotNil(t, resp2)
	require.Equal(t, resp1.Choices[0].Message.Content, resp2.Choices[0].Message.Content)
}

func TestInternalChatCompletionExecutor_CachedNilWorkflowDoesNotPanic(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &contextCapturingProvider{
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

	_, _, cacheType, err := executor.executeChatCompletion(context.Background(), nil, req)
	require.NoError(t, err)
	require.Empty(t, cacheType)
	err = rcm.Close()
	require.NoError(t, err)

	resp, meta, cacheType, err := executor.executeChatCompletion(context.Background(), nil, req)
	require.NoError(t, err)
	require.Equal(t, 1, provider.chatCalls)
	require.NotNil(t, resp)
	require.Equal(t, "chatcmpl-internal-cache-nil-workflow", resp.ID)
	require.Empty(t, meta.ProviderType)
	require.Empty(t, meta.ProviderName)
	require.Equal(t, responsecache.CacheTypeExact, cacheType)
}

func TestInternalChatCompletionExecutor_MarshalFailureFallsBackToNoCacheDispatch(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()

	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := &contextCapturingProvider{
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
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "chatcmpl-internal-marshal-fallback", resp.ID)
	require.Equal(t, 1, provider.chatCalls)
}
