package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/budget"
	"gomodel/internal/core"
)

type countingBudgetChecker struct {
	calls    int
	userPath string
}

func (c *countingBudgetChecker) Check(_ context.Context, userPath string, _ time.Time) error {
	c.calls++
	c.userPath = userPath
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
	if checker.userPath != "/" {
		t.Fatalf("budget user path = %q, want /", checker.userPath)
	}
}

func TestBatchBudgetEnforcerUsesResolvedWorkflow(t *testing.T) {
	checker := &countingBudgetChecker{}
	enforcer := batchBudgetEnforcer(checker)
	if enforcer == nil {
		t.Fatal("batchBudgetEnforcer() = nil, want function")
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
	enforcer := batchBudgetEnforcer(checker)
	if enforcer == nil {
		t.Fatal("batchBudgetEnforcer() = nil, want function")
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

func TestBudgetExceededResponseIncludesRetryAfter(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := budgetCheckError(&budget.ExceededError{
		Result: budget.CheckResult{
			Budget: budget.Budget{
				UserPath:      "/",
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
