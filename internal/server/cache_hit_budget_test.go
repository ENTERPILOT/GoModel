package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/cache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/responsecache"
)

// cacheHitBudgetFake admits every request and reports a billed budget as
// exhausted for cache hits when exceeded is set.
type cacheHitBudgetFake struct {
	exceeded bool
	calls    int
	subjects budget.Subjects
}

func (f *cacheHitBudgetFake) Check(context.Context, budget.Subjects, time.Time) error { return nil }

func (f *cacheHitBudgetFake) CheckCacheHit(_ context.Context, subjects budget.Subjects, now time.Time) error {
	f.calls++
	f.subjects = subjects
	if !f.exceeded {
		return nil
	}
	return &budget.ExceededError{Result: budget.CheckResult{
		Budget:    budget.Budget{Scope: budget.ScopeUserPath, Subject: "/acme", CostBasis: budget.CostBasisBilled, PeriodSeconds: budget.PeriodDailySeconds, Amount: 1},
		PeriodEnd: now.Add(time.Hour),
		Spent:     1,
		HasUsage:  true,
	}}
}

func TestHandleWithCacheChecksBilledBudgetsBeforeReplay(t *testing.T) {
	budgetOff := core.DefaultWorkflowFeatures()
	budgetOff.Budget = false
	tests := []struct {
		name       string
		exceeded   bool
		workflow   *core.Workflow
		wantStatus int
		wantCalls  int
	}{
		{name: "exhausted billed budget rejects the hit", exceeded: true, wantStatus: http.StatusTooManyRequests, wantCalls: 1},
		{name: "budget left serves the hit", wantStatus: http.StatusOK, wantCalls: 1},
		{
			name:       "workflow without budget skips the check",
			exceeded:   true,
			workflow:   &core.Workflow{Policy: &core.ResolvedWorkflowPolicy{VersionID: "v1", Features: budgetOff}},
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cache.NewMapStore()
			t.Cleanup(func() { _ = store.Close() })
			mw := responsecache.NewResponseCacheMiddlewareWithStore(store, time.Hour)

			req := &core.ChatRequest{Model: "gpt-4o-mini", Messages: []core.Message{{Role: "user", Content: "billed-hit"}}}
			body, err := marshalRequestBody(req)
			require.NoError(t, err)
			primeCtx, _ := echotest.Post(t, "/v1/chat/completions", body)
			err = mw.HandleRequest(primeCtx, body, func() error {
				return primeCtx.JSON(http.StatusOK, map[string]any{"id": "chatcmpl-1", "object": "chat.completion", "model": "gpt-4o-mini", "choices": []any{}})
			})
			require.NoError(t, err)
			require.NoError(t, mw.Close()) // flushes the async cache write

			checker := &cacheHitBudgetFake{exceeded: tt.exceeded}
			s := &translatedInferenceService{responseCache: mw, budgetChecker: checker}
			c, rec := echotest.Post(t, "/v1/chat/completions", body)
			c.SetRequest(c.Request().WithContext(core.WithRequestLabels(c.Request().Context(), []string{"acme"})))
			err = handleWithCache(s, c, req, tt.workflow, func(*echo.Context, *core.ChatRequest, *core.Workflow) error {
				t.Fatal("a cached request must not dispatch")
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantCalls, checker.calls)
			if tt.wantStatus == http.StatusTooManyRequests {
				assert.Contains(t, rec.Body.String(), "budget_exceeded")
				assert.Empty(t, rec.Header().Get("X-Cache"))
			}
			if tt.wantCalls > 0 {
				// Cache hits are not routed, so provider budgets stay out.
				assert.Empty(t, checker.subjects.Provider)
				assert.Equal(t, []string{"acme"}, checker.subjects.Labels)
			}
		})
	}
}
