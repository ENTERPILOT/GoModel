package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/budget"
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

// exceededBudgetChecker refuses every request, standing in for a path that is
// already over budget.
type exceededBudgetChecker struct{ calls int }

func (c *exceededBudgetChecker) Check(context.Context, budget.Subjects, time.Time) error {
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

			first := doChat(t, handler, chatBody)
			if action == "block" && first.Code != http.StatusBadRequest {
				t.Fatalf("first status = %d, want 400 (%s)", first.Code, first.Body.String())
			}
			if action == "respond" && first.Code != http.StatusOK {
				t.Fatalf("first status = %d, want 200 (%s)", first.Code, first.Body.String())
			}

			second := doChat(t, handler, chatBody)
			if second.Code != http.StatusTooManyRequests {
				t.Fatalf("second status = %d, want 429 — the guardrail decision did not consume the limit (%s)", second.Code, second.Body.String())
			}
			if !strings.Contains(second.Body.String(), "rate_limit_exceeded") {
				t.Fatalf("second body = %s, want rate_limit_exceeded", second.Body.String())
			}
			if promptHookCalls != 1 {
				t.Fatalf("prompt hook ran %d times, want 1 — the rejected request still paid for the chain", promptHookCalls)
			}
		})
	}

	t.Run("over budget refuses before the chain", func(t *testing.T) {
		promptHookCalls = 0
		handler := phaseHandler(t, phaseProvider(), countingPromptChains(t, "respond"))
		checker := &exceededBudgetChecker{}
		handler.budgetChecker = checker

		rec := doChat(t, handler, chatBody)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "budget_exceeded") {
			t.Fatalf("body = %s, want budget_exceeded", rec.Body.String())
		}
		if promptHookCalls != 0 {
			t.Fatalf("prompt hook ran %d times over budget, want 0", promptHookCalls)
		}
		if checker.calls != 1 {
			t.Fatalf("budget checked %d times, want 1", checker.calls)
		}
	})

	t.Run("an allowed request is counted once", func(t *testing.T) {
		promptHookCalls = 0
		handler := phaseHandler(t, phaseProvider(), newSystemPromptChains(t, "be safe"))
		handler.rateLimiter = newTestRateLimitService(t, rateLimitRuleWithRequests("/", 2))

		for i := range 2 {
			if rec := doChat(t, handler, chatBody); rec.Code != http.StatusOK {
				t.Fatalf("request %d status = %d, want 200 (%s)", i+1, rec.Code, rec.Body.String())
			}
		}
		if rec := doChat(t, handler, chatBody); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("third status = %d, want 429 (%s)", rec.Code, rec.Body.String())
		}
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
		allowUnguarded  bool
		wantRefused     bool
		wantErrContains string
	}{
		{name: "guardrail workflow refuses", chains: chains, wantRefused: true, wantErrContains: "passthrough_guardrails_unsupported"},
		{name: "opt-out allows", chains: chains, allowUnguarded: true},
		{name: "no chains allows", chains: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &passthroughService{
				pluginChains:              staticChainsResolver{chains: tt.chains},
				allowUnguardedPassthrough: tt.allowUnguarded,
			}
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/p/openai/v1/chat/completions", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if got := svc.guardrailWorkflowApplies(c); got != tt.wantRefused {
				t.Fatalf("guardrailWorkflowApplies() = %v, want %v", got, tt.wantRefused)
			}
			if !tt.wantRefused {
				return
			}
			if err := handleError(c, guardrailsBypassedError("openai")); err != nil {
				t.Fatalf("handleError() error = %v", err)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantErrContains) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tt.wantErrContains)
			}
		})
	}
}

// admitOnce grants one admission per request even though translated inference
// reaches it both before the prompt phase and from dispatch.
func TestAdmitOnceGrantsASingleAdmission(t *testing.T) {
	service := newTestRateLimitService(t, rateLimitRuleWithRequests("/team", 1))
	c, _ := newRateLimitTestContext("/team")

	if _, err := admitOnce(c, service, nil, rateLimitRoute{}); err != nil {
		t.Fatalf("first admitOnce() error = %v", err)
	}
	if _, err := admitOnce(c, service, nil, rateLimitRoute{}); err != nil {
		t.Fatalf("second admitOnce() error = %v — the request was counted twice", err)
	}
	releaseAdmission(c)
	releaseAdmission(c)

	other, _ := newRateLimitTestContext("/team")
	if _, err := admitOnce(other, service, nil, rateLimitRoute{}); err == nil {
		t.Fatal("a second request was admitted under a limit of 1")
	}
}
