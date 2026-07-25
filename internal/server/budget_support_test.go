package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/budget"
	"github.com/enterpilot/gomodel/internal/core"
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
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(core.WithWorkflow(req.Context(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Budget: false,
			},
		},
	}))
	c := e.NewContext(req, httptest.NewRecorder())
	checker := &countingBudgetChecker{}

	if err := enforceBudget(c, checker); err != nil {
		t.Fatalf("enforceBudget returned error: %v", err)
	}
	if checker.calls != 0 {
		t.Fatalf("budget checker was called %d times, want 0", checker.calls)
	}
}

func TestEnforceBudgetDefaultsEnabledWithoutWorkflow(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	checker := &countingBudgetChecker{}

	if err := enforceBudget(c, checker); err != nil {
		t.Fatalf("enforceBudget returned error: %v", err)
	}
	if checker.calls != 1 {
		t.Fatalf("budget checker was called %d times, want 1", checker.calls)
	}
	if checker.subjects.UserPath != "/" {
		t.Fatalf("budget user path = %q, want /", checker.subjects.UserPath)
	}
	if len(checker.subjects.Labels) != 0 {
		t.Fatalf("budget labels = %v, want none for an untagged request", checker.subjects.Labels)
	}
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
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx := core.WithRequestLabels(req.Context(), tt.labels)
			if tt.userPath != "" {
				ctx = core.WithEffectiveUserPath(ctx, tt.userPath)
			}
			c := e.NewContext(req.WithContext(ctx), httptest.NewRecorder())
			checker := &countingBudgetChecker{}

			if err := enforceBudget(c, checker); err != nil {
				t.Fatalf("enforceBudget returned error: %v", err)
			}
			if checker.subjects.UserPath != tt.wantPath {
				t.Fatalf("budget user path = %q, want %q", checker.subjects.UserPath, tt.wantPath)
			}
			if !slices.Equal(checker.subjects.Labels, tt.wantLabels) {
				t.Fatalf("budget labels = %v, want %v", checker.subjects.Labels, tt.wantLabels)
			}
		})
	}
}

func TestBatchBudgetEnforcerUsesResolvedWorkflow(t *testing.T) {
	checker := &countingBudgetChecker{}
	enforcer := batchAdmissionEnforcer(nil, checker)
	if enforcer == nil {
		t.Fatal("batchAdmissionEnforcer() = nil, want function")
	}

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: false,
			},
		},
	})

	if err := enforcer(ctx); err != nil {
		t.Fatalf("batch budget enforcer returned error: %v", err)
	}
	if checker.calls != 0 {
		t.Fatalf("budget checker was called %d times, want 0", checker.calls)
	}
}

func TestBatchBudgetEnforcerInvokesCheckerWhenEnabled(t *testing.T) {
	checker := &countingBudgetChecker{}
	enforcer := batchAdmissionEnforcer(nil, checker)
	if enforcer == nil {
		t.Fatal("batchAdmissionEnforcer() = nil, want function")
	}

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: true,
			},
		},
	})

	if err := enforcer(ctx); err != nil {
		t.Fatalf("batch budget enforcer returned error: %v", err)
	}
	if checker.calls != 1 {
		t.Fatalf("budget checker was called %d times, want 1", checker.calls)
	}
}

func TestBatchAdmissionEnforcerAppliesRateLimits(t *testing.T) {
	checker := &countingBudgetChecker{}
	limiter := newTestRateLimitService(t, rateLimitRuleWithRequests("/", 1))
	enforcer := batchAdmissionEnforcer(limiter, checker)
	if enforcer == nil {
		t.Fatal("batchAdmissionEnforcer() = nil, want function")
	}

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Policy: &core.ResolvedWorkflowPolicy{
			VersionID: "workflow-v1",
			Features: core.WorkflowFeatures{
				Usage:  true,
				Budget: true,
			},
		},
	})

	if err := enforcer(ctx); err != nil {
		t.Fatalf("first batch submission rejected: %v", err)
	}
	if checker.calls != 1 {
		t.Fatalf("budget checker was called %d times, want 1", checker.calls)
	}

	err := enforcer(ctx)
	if err == nil {
		t.Fatal("second batch submission admitted over the request window")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error %T does not unwrap to GatewayError", err)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", gatewayErr.HTTPStatusCode())
	}
	if checker.calls != 1 {
		t.Fatalf("budget checker was called %d times after a rate-limited submission, want 1 (rate limits check first)", checker.calls)
	}
}

func TestBudgetExceededResponseIncludesRetryAfter(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

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
	if err := handleError(c, err); err != nil {
		t.Fatalf("handleError() error = %v", err)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After header is empty")
	}
	seconds, parseErr := strconv.Atoi(retryAfter)
	if parseErr != nil {
		t.Fatalf("Retry-After = %q, want delay seconds", retryAfter)
	}
	if seconds <= 0 || seconds > 300 {
		t.Fatalf("Retry-After = %d, want between 1 and 300", seconds)
	}
}

func TestBudgetCheckFailedResponseMapping(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := budgetCheckError(errors.New("backend details should not leak"))
	if err := handleError(c, err); err != nil {
		t.Fatalf("handleError() error = %v", err)
	}

	if rec.Code == http.StatusTooManyRequests {
		t.Fatalf("status = %d, want non-rate-limit budget_check_failed response", rec.Code)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"budget_check_failed"`) {
		t.Fatalf("body = %s, want budget_check_failed code", body)
	}
	if !strings.Contains(body, `"message":"budget check failed"`) {
		t.Fatalf("body = %s, want generic budget check message", body)
	}
	if strings.Contains(body, "backend details should not leak") {
		t.Fatalf("body leaked wrapped error detail: %s", body)
	}
}
