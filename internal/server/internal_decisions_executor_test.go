package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/usage"
)

// pluginContext is the context a plugin's decision call arrives with.
func pluginContext() context.Context {
	ctx := core.WithRequestOrigin(context.Background(), core.RequestOriginPlugin)
	return core.WithEffectiveUserPath(ctx, "/team/guardrails/policy")
}

// A virtual model resolves to its target, the body reaches the provider
// under the concrete model name, and the call is accounted and audited under
// /v1/decisions with the plugin's origin and user path.
func TestInternalDecisions_ResolvesAccountsAndAudits(t *testing.T) {
	provider := newDecisionsTestProvider()
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	auditLogger := &capturingAuditLogger{config: auditlog.Config{Enabled: true}}
	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{
		ModelResolver: decisionAliases,
		UsageLogger:   usageLogger,
		AuditLogger:   auditLogger,
	})

	answer, err := executor.Decide(pluginContext(), []byte(decisionBody("classifier")))
	require.NoError(t, err)
	assert.Contains(t, string(answer), `"probability":0.98`)
	assert.Equal(t, []string{"openai openai gpt-6-luna"}, provider.calls)
	assert.Equal(t, "gpt-6-luna", provider.bodies[0]["model"])

	require.Len(t, usageLogger.entries, 1)
	usageEntry := usageLogger.entries[0]
	assert.Equal(t, 40, usageEntry.InputTokens)
	assert.Equal(t, "/v1/decisions", usageEntry.Endpoint)
	assert.Equal(t, "openai", usageEntry.Provider)
	assert.Equal(t, "/team/guardrails/policy", usageEntry.UserPath)

	require.Len(t, auditLogger.entries, 1)
	auditEntry := auditLogger.entries[0]
	assert.Equal(t, "/v1/decisions", auditEntry.Path)
	assert.Equal(t, "/team/guardrails/policy", auditEntry.UserPath)
	assert.Equal(t, "classifier", auditEntry.RequestedModel)
	assert.Equal(t, http.StatusOK, auditEntry.StatusCode)
}

// A model without a Decisions API, or a gateway without a Decisions
// provider, is refused before anything is sent.
func TestInternalDecisions_RefusesModelsThatCannotDecide(t *testing.T) {
	provider := newDecisionsTestProvider()
	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{})
	_, err := executor.Decide(pluginContext(), []byte(decisionBody("openrouter/typesafe/jev-1.13")))
	require.ErrorContains(t, err, "which has no decisions API")
	assert.Empty(t, provider.calls)

	_, err = executor.Decide(pluginContext(), []byte(`{"input":"x","questions":[]}`))
	require.ErrorContains(t, err, "model is required")

	none := newScriptedDecisionsProvider(map[string]string{"openrouter/typesafe/jev-1.13": "openrouter"})
	_, err = NewInternalDecisionsExecutor(none, InternalChatCompletionExecutorConfig{}).Decide(pluginContext(), []byte(decisionBody("openrouter/typesafe/jev-1.13")))
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusNotFound, gatewayErr.HTTPStatusCode())
}

// A failed primary fails over to the next target that answers Decisions,
// passing over one without a Decisions API, and usage names the target that
// answered.
func TestInternalDecisions_FailsOver(t *testing.T) {
	provider := newDecisionsTestProvider()
	provider.statuses["kev-latest"] = http.StatusServiceUnavailable
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{
		FailoverResolver: failoverResolverStub{selectors: []core.ModelSelector{
			{Provider: "openrouter", Model: "typesafe/jev-1.13"},
			{Provider: "openai", Model: "gpt-6-luna"},
		}},
		UsageLogger: usageLogger,
	})

	_, err := executor.Decide(pluginContext(), []byte(decisionBody("kev/kev-latest")))
	require.NoError(t, err)
	assert.Equal(t, []string{"kev jev kev-latest", "openai openai gpt-6-luna"}, provider.calls)
	require.Len(t, usageLogger.entries, 1)
	assert.Equal(t, "openai", usageLogger.entries[0].Provider)
}

// Images go to the first target that reads them, and are refused when none
// does.
func TestInternalDecisions_RoutesImagesToModelsThatReadThem(t *testing.T) {
	body := []byte(fmt.Sprintf(decisionImageBody, "kev/kev-latest"))

	provider := newVisionDecisionsProvider()
	_, err := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{}).Decide(pluginContext(), body)
	require.ErrorContains(t, err, "does not read images")
	assert.Empty(t, provider.calls)

	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{
		FailoverResolver: failoverResolverStub{selectors: []core.ModelSelector{{Provider: "jev", Model: "jev-latest"}}},
	})
	_, err = executor.Decide(pluginContext(), body)
	require.NoError(t, err)
	assert.Equal(t, []string{"jev jev jev-latest"}, provider.calls)
}

// A model the caller may not use is refused.
func TestInternalDecisions_ChecksModelAccess(t *testing.T) {
	provider := newDecisionsTestProvider()
	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{
		ModelAuthorizer: denyingAuthorizer{"kev/kev-latest": true},
	})
	_, err := executor.Decide(pluginContext(), []byte(decisionBody("kev/kev-latest")))
	require.ErrorContains(t, err, "is not allowed")
	assert.Empty(t, provider.calls)
}

// The same question about the same input is answered from the exact cache
// the second time, and only the provider round trip is accounted.
func TestInternalDecisions_RoutesThroughResponseCache(t *testing.T) {
	store := cache.NewMapStore()
	defer store.Close()
	rcm := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)
	provider := newDecisionsTestProvider()
	usageLogger := &collectingUsageLogger{config: usage.Config{Enabled: true}}
	executor := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{ResponseCache: rcm, UsageLogger: usageLogger})

	first, err := executor.Decide(pluginContext(), []byte(decisionBody("kev/kev-latest")))
	require.NoError(t, err)
	require.NoError(t, rcm.Close())
	second, err := executor.Decide(pluginContext(), []byte(decisionBody("kev/kev-latest")))
	require.NoError(t, err)

	assert.JSONEq(t, string(first), string(second))
	assert.Len(t, provider.calls, 1)
	assert.Len(t, usageLogger.entries, 1)
}

// A pinned jev version the catalog does not list goes to the jev provider it
// names, as on POST /v1/decisions, and access to it is still checked.
func TestInternalDecisions_RoutesUnlistedJevVersions(t *testing.T) {
	provider := newDecisionsTestProvider()
	_, err := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{}).Decide(pluginContext(), []byte(decisionBody("kev/kev-1.0.0")))
	require.NoError(t, err)
	assert.Equal(t, []string{"kev jev kev-1.0.0"}, provider.calls)

	denied := NewInternalDecisionsExecutor(provider, InternalChatCompletionExecutorConfig{
		ModelAuthorizer: denyingAuthorizer{"kev/kev-1.0.0": true},
	})
	_, err = denied.Decide(pluginContext(), []byte(decisionBody("kev/kev-1.0.0")))
	require.ErrorContains(t, err, "is not allowed")
	assert.Len(t, provider.calls, 1)
}
