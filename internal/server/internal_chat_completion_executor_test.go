package server

import (
	"context"
	"testing"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type contextCapturingProvider struct {
	capturingProvider
	capturedCtx context.Context
}

func (p *contextCapturingProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	p.capturedCtx = ctx
	return p.capturingProvider.ChatCompletion(ctx, req)
}

type countingTranslatedRequestPatcher struct {
	chatCalls int
}

func (p *countingTranslatedRequestPatcher) PatchChatRequest(_ context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	p.chatCalls++
	cloned := *req
	cloned.Messages = append([]core.Message{{Role: "system", Content: "patched"}}, req.Messages...)
	return &cloned, nil
}

func (p *countingTranslatedRequestPatcher) PatchResponsesRequest(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return req, nil
}

func TestInternalChatCompletionExecutor_UsesTranslatedPathAndSkipsGuardrailPatching(t *testing.T) {
	logger := &capturingAuditLogger{
		config: auditlog.Config{Enabled: true},
	}
	patcher := &countingTranslatedRequestPatcher{}
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

	var capturedSelector core.ExecutionPlanSelector
	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{
		ExecutionPolicyResolver: requestExecutionPolicyResolverFunc(func(selector core.ExecutionPlanSelector) (*core.ResolvedExecutionPolicy, error) {
			capturedSelector = selector
			return &core.ResolvedExecutionPolicy{
				VersionID:      "workflow-guardrail",
				ScopeUserPath:  selector.UserPath,
				GuardrailsHash: "hash-should-be-cleared",
				Features: core.ExecutionFeatures{
					Cache:      true,
					Audit:      true,
					Usage:      true,
					Guardrails: true,
					Fallback:   true,
				},
			}, nil
		}),
		TranslatedRequestPatcher: patcher,
		AuditLogger:              logger,
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
	if patcher.chatCalls != 0 {
		t.Fatalf("patcher chat calls = %d, want 0 because guardrails should be skipped", patcher.chatCalls)
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
	if entry.ExecutionPlanVersionID != "workflow-guardrail" {
		t.Fatalf("audit execution plan version = %q, want workflow-guardrail", entry.ExecutionPlanVersionID)
	}
	if entry.Data == nil || entry.Data.ExecutionFeatures == nil {
		t.Fatalf("audit execution features = %#v, want populated snapshot", entry.Data)
	}
	if entry.Data.ExecutionFeatures.Guardrails {
		t.Fatalf("audit guardrails feature = true, want false for internal guardrail calls")
	}
}

func TestInternalChatCompletionExecutor_DoesNotReuseParentExecutionPlanResolution(t *testing.T) {
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

	parentCtx := core.WithExecutionPlan(context.Background(), &core.ExecutionPlan{
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
	if entry.Model != "openai/gpt-4o-mini" {
		t.Fatalf("audit requested model = %q, want openai/gpt-4o-mini", entry.Model)
	}
	if entry.ResolvedModel != "openai/gpt-4o-mini" {
		t.Fatalf("audit resolved model = %q, want openai/gpt-4o-mini", entry.ResolvedModel)
	}
}
