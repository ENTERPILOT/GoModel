package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
)

func extensionContext() context.Context {
	ctx := core.WithRequestOrigin(context.Background(), core.RequestOriginPlugin)
	return core.WithEffectiveUserPath(ctx, "/team/extensions/router")
}

func admissionChatProvider() *contextCapturingProvider {
	return &contextCapturingProvider{
		supportedModels: []string{"gpt-6-luna"},
		providerTypes:   map[string]string{"gpt-6-luna": "openai"},
		response: &core.ChatResponse{
			ID:       "chatcmpl-1",
			Model:    "gpt-6-luna",
			Provider: "openai",
			Choices:  []core.Choice{{FinishReason: "stop", Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}},
		},
	}
}

func requireGatewayStatus(t *testing.T, err error, status int) {
	t.Helper()
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, status, gatewayErr.HTTPStatusCode())
}

// With admission configured, an exhausted rate limit or budget rejects the
// call before it reaches the provider.
func TestInternalChatCompletionExecutor_AdmitsWhenConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  InternalChatCompletionExecutorConfig
	}{
		{name: "rate limit", cfg: InternalChatCompletionExecutorConfig{RateLimiter: rejectingRateLimiter{}}},
		{name: "budget", cfg: InternalChatCompletionExecutorConfig{BudgetChecker: exceededBudgetChecker{scope: budget.ScopeUserPath}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := admissionChatProvider()
			executor := NewInternalChatCompletionExecutor(provider, tt.cfg)
			_, err := executor.ChatCompletion(extensionContext(), &core.ChatRequest{Model: "gpt-6-luna"})
			requireGatewayStatus(t, err, http.StatusTooManyRequests)
			assert.Zero(t, provider.chatCalls)
		})
	}
}

// The budget is checked against the call's own user path and routed provider.
func TestInternalChatCompletionExecutor_ChecksBudgetSubjects(t *testing.T) {
	provider := admissionChatProvider()
	checker := &countingBudgetChecker{}
	executor := NewInternalChatCompletionExecutor(provider, InternalChatCompletionExecutorConfig{BudgetChecker: checker})

	_, err := executor.ChatCompletion(extensionContext(), &core.ChatRequest{Model: "gpt-6-luna"})
	require.NoError(t, err)
	assert.Equal(t, 1, checker.calls)
	assert.Equal(t, "/team/extensions/router", checker.subjects.UserPath)
	assert.Equal(t, 1, provider.chatCalls)
}

func TestInternalDecisions_AdmitsWhenConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  InternalChatCompletionExecutorConfig
	}{
		{name: "rate limit", cfg: InternalChatCompletionExecutorConfig{RateLimiter: rejectingRateLimiter{}}},
		{name: "budget", cfg: InternalChatCompletionExecutorConfig{BudgetChecker: exceededBudgetChecker{scope: budget.ScopeUserPath}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newDecisionsTestProvider()
			executor := NewInternalDecisionsExecutor(provider, tt.cfg)
			_, err := executor.Decide(extensionContext(), []byte(decisionBody("gpt-6-luna")))
			requireGatewayStatus(t, err, http.StatusTooManyRequests)
			assert.Empty(t, provider.calls)
		})
	}
}

// A saturated primary provider defers to failover as over HTTP: the call
// skips the primary and is served by the configured failover target.
func TestInternalDecisions_SaturatedPrimaryFailsOver(t *testing.T) {
	provider := newDecisionsTestProvider()
	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{
		FailoverResolver: failoverResolverStub{selectors: []core.ModelSelector{{Provider: "openai", Model: "gpt-6-luna"}}},
		RateLimiter:      newTestRateLimitService(t, rateLimitProviderRule("kev", 1)),
	})

	_, err := executor.Decide(extensionContext(), []byte(decisionBody("kev/kev-latest")))
	require.NoError(t, err)
	_, err = executor.Decide(extensionContext(), []byte(decisionBody("kev/kev-latest")))
	require.NoError(t, err)
	assert.Equal(t, []string{"kev jev kev-latest", "openai openai gpt-6-luna"}, provider.calls)
}
