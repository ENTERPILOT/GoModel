package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type countingBudgetChecker struct {
	calls    int
	subjects budget.Subjects
}

func (c *countingBudgetChecker) Check(_ context.Context, subjects budget.Subjects, _ time.Time) error {
	c.calls++
	c.subjects = subjects
	return nil
}

func TestEnforceBudgetSkipsWhenWorkflowBudgetDisabled(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	c.SetRequest(c.Request().WithContext(core.WithWorkflow(c.Request().Context(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Budget: false,
			},
		},
	})))
	checker := &countingBudgetChecker{}
	err := enforceBudget(c, checker)
	require.NoError(t, err)
	require.Equal(t, 0, checker.calls)
}

func TestEnforceBudgetDefaultsEnabledWithoutWorkflow(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	checker := &countingBudgetChecker{}
	err := enforceBudget(c, checker)
	require.NoError(t, err)
	require.Equal(t, 1, checker.calls)
	require.Equal(t, "/", checker.subjects.UserPath)
	require.Empty(t, checker.subjects.Labels)
}

// Label budgets can only match if the labels tagging attached at ingress reach
// the checker alongside the user path.
func TestEnforceBudgetPassesRequestLabelsAndUserPath(t *testing.T) {
	tests := []struct {
		name       string
		userPath   string
		labels     []string
		wantPath   string
		wantLabels []string
	}{
		{
			name:       "labels and a bound user path",
			userPath:   "/team/alpha",
			labels:     []string{"Mobile-App-iOS", "prod"},
			wantPath:   "/team/alpha",
			wantLabels: []string{"Mobile-App-iOS", "prod"},
		},
		{
			name:       "labels without a user path fall back to the root",
			labels:     []string{"prod"},
			wantPath:   "/",
			wantLabels: []string{"prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := echotest.Post(t, "/v1/chat/completions", nil)
			ctx := core.WithRequestLabels(c.Request().Context(), tt.labels)
			if tt.userPath != "" {
				ctx = core.WithEffectiveUserPath(ctx, tt.userPath)
			}
			c.SetRequest(c.Request().WithContext(ctx))
			checker := &countingBudgetChecker{}
			err := enforceBudget(c, checker)
			require.NoError(t, err)
			require.Equal(t, tt.wantPath, checker.subjects.UserPath)
			require.True(t, slices.Equal(checker.subjects.Labels, tt.wantLabels), "budget labels = %v, want %v", checker.subjects.Labels, tt.wantLabels)
		})
	}
}

func TestBatchBudgetEnforcerUsesResolvedWorkflow(t *testing.T) {
	checker := &countingBudgetChecker{}
	enforcer := batchAdmissionEnforcer(nil, checker)
	require.NotNil(t, enforcer)

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: false,
			},
		},
	})
	err := enforcer(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, checker.calls)
}

func TestBatchBudgetEnforcerInvokesCheckerWhenEnabled(t *testing.T) {
	checker := &countingBudgetChecker{}
	enforcer := batchAdmissionEnforcer(nil, checker)
	require.NotNil(t, enforcer)

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: true,
			},
		},
	})
	err := enforcer(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, checker.calls)
}

func TestBatchAdmissionEnforcerAppliesRateLimits(t *testing.T) {
	checker := &countingBudgetChecker{}
	limiter := newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))
	enforcer := batchAdmissionEnforcer(limiter, checker)
	require.NotNil(t, enforcer)

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: true,
			},
		},
	})
	err := enforcer(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, checker.calls)

	err = enforcer(ctx)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusTooManyRequests, gatewayErr.HTTPStatusCode())
	require.Equal(t, 1, checker.calls)
}

func TestBudgetExceededResponseIncludesRetryAfter(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)

	err := budgetCheckError(&budget.ExceededError{
		Result: budget.CheckResult{
			Budget: budget.Budget{
				Scope:         budget.ScopeUserPath,
				Subject:       "/",
				PeriodSeconds: budget.PeriodDailySeconds,
				Amount:        1,
			},
			PeriodEnd: time.Now().UTC().Add(5 * time.Minute),
			Spent:     1,
		},
	})
	err = handleError(c, err)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	retryAfter := rec.Header().Get("Retry-After")
	require.NotEmpty(t, retryAfter)

	seconds, parseErr := strconv.Atoi(retryAfter)
	require.NoError(t, parseErr, "Retry-After = %q, want delay seconds", retryAfter)
	require.Greater(t, seconds, 0)
	require.LessOrEqual(t, seconds, 300)
}

func TestBudgetCheckFailedResponseMapping(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)

	err := budgetCheckError(errors.New("backend details should not leak"))
	err = handleError(c, err)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `"code":"budget_check_failed"`)
	require.Contains(t, body, `"message":"budget check failed"`)
	require.NotContains(t, body, "backend details should not leak", "body leaked wrapped error detail: %s", body)
}
