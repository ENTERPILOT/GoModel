package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// countingPromptPlugin blocks (or answers) every prompt and counts how often
// its prompt hook ran, standing in for a guardrail that spends provider money
// deciding the request (llm_judge).
type countingPromptPlugin struct {
	calls  *int
	action string
}

// promptHookCalls is the shared counter of the plugin instances the guardrail
// service builds; each test run installs its own.
var promptHookCalls int

func newCountingPromptPlugin() pluginapi.Plugin {
	return &countingPromptPlugin{calls: &promptHookCalls}
}

func (p *countingPromptPlugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:         "counting_prompt",
		Kinds:        []pluginapi.Kind{pluginapi.KindPrompt},
		Guardrail:    true,
		ConfigSchema: []pluginapi.Field{{Key: "action", Input: pluginapi.InputText}},
	}
}

func (p *countingPromptPlugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	var cfg struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	p.action = cfg.Action
	return nil
}

func (p *countingPromptPlugin) Close(context.Context) error { return nil }

func (p *countingPromptPlugin) OnPrompt(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	*p.calls++
	if p.action == "respond" {
		return pluginapi.Respond("I cannot help with that."), nil
	}
	return pluginapi.Block(0, "policy", "blocked by test"), nil
}

func countingPromptChains(t *testing.T, action string) *plugins.Chains {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"action": action})
	return newGuardrailChains(t, nil, []guardrails.StepReference{{Ref: "counter", Step: 0}},
		[]func() pluginapi.Plugin{newCountingPromptPlugin},
		guardrails.Definition{Name: "counter", Type: "counting_prompt", Config: raw})
}

// overBudgetCounter refuses and counts every request, standing in for a path
// that is already over budget.
type overBudgetCounter struct{ calls int }

func (c *overBudgetCounter) Check(context.Context, budget.Subjects, time.Time) error {
	c.calls++
	return &budget.ExceededError{Result: budget.CheckResult{
		Budget:    budget.Budget{Scope: budget.ScopeUserPath, Subject: "/", PeriodSeconds: budget.PeriodDailySeconds, Amount: 1},
		PeriodEnd: time.Now().UTC().Add(time.Hour),
		Spent:     2,
	}}
}

// TestGuardrailDecisionsAreAdmitted covers the admission ordering around the
// prompt phase: a request a guardrail blocks or answers is counted against the
// rate limit like any other, and an over-limit or over-budget request is
// refused before the chain (and its provider spend) runs at all.
func TestGuardrailDecisionsAreAdmitted(t *testing.T) {
	for _, action := range []string{"block", "respond"} {
		t.Run(action+" consumes the rate limit", func(t *testing.T) {
			promptHookCalls = 0
			handler := phaseHandler(t, phaseProvider(), countingPromptChains(t, action))
			handler.rateLimiter = newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))

			wantFirst := http.StatusBadRequest
			if action == "respond" {
				wantFirst = http.StatusOK
			}
			first := doChat(t, handler, chatBody)
			require.Equal(t, wantFirst, first.Code, first.Body.String())

			second := doChat(t, handler, chatBody)
			require.Equal(t, http.StatusTooManyRequests, second.Code, "the guardrail decision did not consume the limit: %s", second.Body.String())
			assert.Contains(t, second.Body.String(), "rate_limit_exceeded")
			assert.Equal(t, 1, promptHookCalls, "the rejected request still paid for the chain")
		})
	}

	t.Run("over budget refuses before the chain", func(t *testing.T) {
		promptHookCalls = 0
		handler := phaseHandler(t, phaseProvider(), countingPromptChains(t, "respond"))
		checker := &overBudgetCounter{}
		handler.budgetChecker = checker

		rec := doChat(t, handler, chatBody)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "budget_exceeded")
		assert.Zero(t, promptHookCalls, "prompt hook ran over budget")
		assert.Equal(t, 1, checker.calls, "budget checks")
	})

	t.Run("an allowed request is counted once", func(t *testing.T) {
		promptHookCalls = 0
		handler := phaseHandler(t, phaseProvider(), newSystemPromptChains(t, "be safe"))
		handler.rateLimiter = newTestRateLimitService(t, rateLimitRuleWithRequests("/", 2))

		for i := range 2 {
			rec := doChat(t, handler, chatBody)
			require.Equal(t, http.StatusOK, rec.Code, "request %d: %s", i+1, rec.Body.String())
		}
		rec := doChat(t, handler, chatBody)
		assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	})
}

// TestPassthroughRefusesWhenGuardrailsApply covers the passthrough policy
// gap: /p/{provider}/... cannot run guardrail chains over provider-native
// bodies, so a caller a guardrail workflow applies to is refused rather than
// silently served without the policy.
func TestPassthroughRefusesWhenGuardrailsApply(t *testing.T) {
	chains := countingPromptChains(t, "block")

	tests := []struct {
		name            string
		chains          *plugins.Chains
		configured      bool
		info            *core.PassthroughRouteInfo
		allowUnguarded  bool
		wantRefused     bool
		wantErrContains string
	}{
		{name: "guardrail workflow refuses", chains: chains, wantRefused: true, wantErrContains: "passthrough_guardrails_unsupported"},
		{name: "opt-out allows", chains: chains, allowUnguarded: true},
		{name: "no chains allows", chains: nil},
		// The workflow is matched on the model read from the provider-native
		// body. A body the gateway cannot parse leaves it empty, so a
		// model-scoped guardrail workflow is never matched: refuse rather than
		// let an unreadable body escape the policy.
		{
			name:            "opaque model on an inference call refuses",
			configured:      true,
			info:            &core.PassthroughRouteInfo{GenAIOperation: "chat"},
			wantRefused:     true,
			wantErrContains: "passthrough_guardrails_unsupported",
		},
		{
			name:       "opaque model on a non-inference call allows",
			configured: true,
			info:       &core.PassthroughRouteInfo{},
		},
		// Listing models or managing files runs no guardrail even on /v1, so
		// passthrough takes nothing away.
		{
			name:   "non-inference route allows even with a matching chain",
			chains: chains,
			info:   &core.PassthroughRouteInfo{Model: "gpt-4.1-mini"},
		},
		{
			name:       "known model with no matching workflow allows",
			configured: true,
			info:       &core.PassthroughRouteInfo{GenAIOperation: "chat", Model: "gpt-4.1-mini"},
		},
		// Only a provider that ships a passthrough semantics table names a
		// GenAI operation, so the endpoint path decides for the providers that
		// do not: the refusal must not be escapable by sending the same body to
		// one of them.
		{
			name:            "chat path on a provider without semantics refuses",
			chains:          chains,
			info:            &core.PassthroughRouteInfo{NormalizedEndpoint: "chat/completions", Model: "gpt-4.1-mini"},
			wantRefused:     true,
			wantErrContains: "passthrough_guardrails_unsupported",
		},
		{
			name:            "unreadable model on a native inference path refuses",
			configured:      true,
			info:            &core.PassthroughRouteInfo{NormalizedEndpoint: "v1beta/models/gemini-2.5-flash:generateContent"},
			wantRefused:     true,
			wantErrContains: "passthrough_guardrails_unsupported",
		},
		{
			name:            "legacy completions refuses",
			chains:          chains,
			info:            &core.PassthroughRouteInfo{GenAIOperation: "text_completion", Model: "gpt-4.1-mini"},
			wantRefused:     true,
			wantErrContains: "passthrough_guardrails_unsupported",
		},
		{
			name:   "model listing on a provider without semantics allows",
			chains: chains,
			info:   &core.PassthroughRouteInfo{NormalizedEndpoint: "models"},
		},
		{
			name:   "token counting allows",
			chains: chains,
			info:   &core.PassthroughRouteInfo{NormalizedEndpoint: "messages/count_tokens", Model: "claude-sonnet-4-5"},
		},
		{
			name:   "embeddings allow",
			chains: chains,
			info:   &core.PassthroughRouteInfo{GenAIOperation: "embeddings", Model: "text-embedding-3-small"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := tt.info
			if info == nil {
				info = &core.PassthroughRouteInfo{GenAIOperation: "chat", Model: "gpt-4.1-mini"}
			}
			svc := &passthroughService{
				pluginChains:              staticChainsResolver{chains: tt.chains, configured: tt.configured},
				allowUnguardedPassthrough: tt.allowUnguarded,
			}
			c, rec := echotest.Post(t, "/p/openai/v1/chat/completions", nil)
			require.Equal(t, tt.wantRefused, svc.guardrailWorkflowApplies(c, info))
			if !tt.wantRefused {
				return
			}
			require.NoError(t, handleError(c, guardrailsBypassedError("openai")))
			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantErrContains)
		})
	}
}

// admitOnce grants one admission per request even though translated inference
// reaches it both before the prompt phase and from dispatch.
func TestAdmitOnceGrantsASingleAdmission(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 1))
	c, _ := newRateLimitTestContext(t, "/team")

	_, err := admitOnce(c, service, nil, rateLimitRoute{})
	require.NoError(t, err, "first admitOnce")
	_, err = admitOnce(c, service, nil, rateLimitRoute{})
	require.NoError(t, err, "second admitOnce: the request was counted twice")
	releaseAdmission(c)
	releaseAdmission(c)

	other, _ := newRateLimitTestContext(t, "/team")
	_, err = admitOnce(other, service, nil, rateLimitRoute{})
	assert.Error(t, err, "a second request was admitted under a limit of 1")
}

// Native JSON routes (/v1/systemone, /v1/decisions) run their prompt
// guardrails before dispatch too, so they are admitted before the chain and
// counted once.
func TestNativeJSONGuardrailDecisionsAreAdmitted(t *testing.T) {
	newNativeHandler := func(t *testing.T, chains *plugins.Chains) *Handler {
		t.Helper()
		provider := newScriptedSystemOneProvider(map[string]string{"kev/kev-latest": "jev"})
		handler := newHandler(provider, nil, nil, nil, nil, nil, nil, guardrails.NewWorkflowRequestPatcher(staticChainsResolver{chains: chains}))
		handler.pluginChains = staticChainsResolver{chains: chains}
		handler.rateLimiter = newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))
		return handler
	}
	doSystemOne := func(t *testing.T, handler *Handler) int {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/systemone", systemOneRequest("kev-latest"))
		require.NoError(t, handler.SystemOne(c))
		return rec.Code
	}

	t.Run("a block consumes the rate limit", func(t *testing.T) {
		promptHookCalls = 0
		handler := newNativeHandler(t, countingPromptChains(t, "block"))

		assert.Equal(t, http.StatusBadRequest, doSystemOne(t, handler))
		assert.Equal(t, http.StatusTooManyRequests, doSystemOne(t, handler))
		assert.Equal(t, 1, promptHookCalls, "the rejected request still paid for the chain")
	})

	t.Run("an allowed request is counted once", func(t *testing.T) {
		handler := newNativeHandler(t, newSystemPromptChains(t, "be safe"))

		assert.Equal(t, http.StatusOK, doSystemOne(t, handler))
		assert.Equal(t, http.StatusTooManyRequests, doSystemOne(t, handler))
	})
}
